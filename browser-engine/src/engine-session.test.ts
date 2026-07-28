import assert from "node:assert/strict";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import {
  errors as playwrightErrors,
  type BrowserContext,
  type Page,
} from "playwright-core";

import { BrowserEngine } from "./engine.js";
import { OriginBudgetStore } from "./origin-budget.js";
import { PageContinuationStore } from "./page-continuation.js";
import {
  type BrowserAction,
  ENGINE_CONTRACT_ID,
  type EngineRequest,
  type Identity,
  type ObservationMode,
} from "./protocol.js";

test("retries Session restoration instead of succeeding on about:blank", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const identity = fixtureIdentity(1);
  const continuation = new PageContinuationStore(root);
  await continuation.record(identity, "https://example.com/saved");

  let restoreAttempts = 0;
  const context = new FakeContext(() =>
    new FakePage(async (url) => {
      if (url === "https://example.com/saved") {
        restoreAttempts++;
        if (restoreAttempts === 1) {
          throw new Error("temporary network failure");
        }
      }
    }),
  );
  const engine = createEngine(context, continuation, async () => true);

  const first = await engine.execute(request(identity, "1", "screenshot"));
  assert.equal(first.status, "error");
  if (first.status === "error") {
    assert.equal(first.error.code, "BROWSER_EGRESS_UNAVAILABLE");
    assert.equal(first.error.recoverable, true);
  }
  assert.equal(
    await continuation.lookup(identity),
    "https://example.com/saved",
    "failed restoration must not delete the continuation",
  );

  const second = await engine.execute(request(identity, "2", "screenshot"));
  assert.equal(second.status, "ok");
  if (second.status === "ok") {
    assert.equal(second.observation.origin, "https://example.com");
  }
  assert.equal(restoreAttempts, 2, "retry must attempt restoration again");
  assert.equal(context.createdPages.length, 2);
  assert.equal(context.createdPages[0]?.isClosed(), true);
  assert.equal(context.createdPages[1]?.url(), "https://example.com/saved");
});

test("uses a new Page when the Browser Session changes", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);

  const first = await engine.execute(
    request(fixtureIdentity(1), "1", "screenshot"),
  );
  assert.equal(first.status, "ok");
  const firstSessionPage = context.createdPages[0];
  assert.ok(firstSessionPage);
  firstSessionPage.sessionStorage.set("wizard-step", "session-a");

  const second = await engine.execute(
    request(fixtureIdentity(2), "2", "screenshot"),
  );
  assert.equal(second.status, "ok");
  const secondSessionPage = context.createdPages[1];
  assert.ok(secondSessionPage);
  assert.notEqual(firstSessionPage, secondSessionPage);
  assert.equal(firstSessionPage.isClosed(), true);
  assert.equal(secondSessionPage.sessionStorage.has("wizard-step"), false);
});

test("keeps uncorrelated HTTPS tunnel failures recoverable", async () => {
  for (const healthy of [true, false]) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const context = new FakeContext(() =>
      new FakePage(async (url) => {
        if (url === "https://public.example/redirect") {
          throw new Error(
            "page.goto: net::ERR_TUNNEL_CONNECTION_FAILED at " + url,
          );
        }
      }),
    );
    let probes = 0;
    const engine = createEngine(context, continuation, async () => {
      probes++;
      return healthy;
    });

    const response = await engine.execute(
      request(fixtureIdentity(1), "1", "navigate"),
    );
    assert.equal(response.status, "error");
    if (response.status === "error") {
      assert.equal(response.error.code, "BROWSER_EGRESS_UNAVAILABLE");
      assert.equal(response.error.recoverable, true);
    }
    assert.equal(probes, 0);
  }
});

test("defaults to semantic output and never hashes screenshot bytes", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);

  const semantic = await engine.execute(
    request(identity, "1", "screenshot"),
  );
  assert.equal(semantic.status, "ok");
  if (semantic.status !== "ok") {
    return;
  }
  assert.equal(semantic.observation.screenshot, undefined);
  assert.notEqual(semantic.observation.ax_tree, undefined);
  const page = context.createdPages[0];
  assert.ok(page);
  assert.equal(page.screenshotCalls, 0);

  const firstScreenshot = await engine.execute(
    request(identity, "2", "screenshot", "screenshot"),
  );
  const secondScreenshot = await engine.execute(
    request(identity, "3", "screenshot", "screenshot"),
  );
  assert.equal(firstScreenshot.status, "ok");
  assert.equal(secondScreenshot.status, "ok");
  if (firstScreenshot.status === "ok" && secondScreenshot.status === "ok") {
    assert.notEqual(firstScreenshot.observation.screenshot?.data, undefined);
    assert.notEqual(secondScreenshot.observation.screenshot?.data, undefined);
    assert.notEqual(
      firstScreenshot.observation.screenshot?.data,
      secondScreenshot.observation.screenshot?.data,
    );
    assert.equal(
      firstScreenshot.observation.page_state_id,
      secondScreenshot.observation.page_state_id,
      "page_state_id must be a semantic digest, not a screenshot hash",
    );
  }
});

test("reports semantic extraction timeouts without failing the action", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.ariaError = new playwrightErrors.TimeoutError("aria timeout");
    page.bodyError = new playwrightErrors.TimeoutError("body timeout");
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const response = await engine.execute(
    request(fixtureIdentity(1), "1", "screenshot"),
  );
  assert.equal(response.status, "ok");
  if (response.status === "ok") {
    assert.equal(response.observation.ax_tree, undefined);
    assert.equal(response.observation.dom_diff, undefined);
    assert.equal(response.observation.ax_tree_timed_out, true);
    assert.equal(response.observation.dom_diff_timed_out, true);
  }
});

test("preflight requires Gateway health and produces a bounded blank observation", async () => {
  for (const healthy of [false, true]) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const context = new FakeContext(() => new FakePage());
    let probes = 0;
    const engine = createEngine(context, continuation, async () => {
      probes++;
      return healthy;
    });
    const response = await engine.execute(
      request(fixtureIdentity(1), "1", "preflight", "semantic"),
    );
    assert.equal(probes, 1);
    if (!healthy) {
      assert.equal(response.status, "error");
      if (response.status === "error") {
        assert.equal(response.error.code, "BROWSER_EGRESS_UNAVAILABLE");
      }
      continue;
    }
    assert.equal(response.status, "ok");
    if (response.status === "ok") {
      assert.equal(response.observation.origin, "");
      assert.equal(response.observation.screenshot, undefined);
      assert.ok(response.observation.page_state_id.length <= 256);
    }
  }
});

test("preflight does not suppress restoration for the first real Session action", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const identity = fixtureIdentity(1);
  const continuation = new PageContinuationStore(root);
  await continuation.record(identity, "https://example.com/saved");
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);

  const preflight = await engine.execute(
    request(identity, "1", "preflight", "semantic"),
  );
  assert.equal(preflight.status, "ok");
  if (preflight.status === "ok") {
    assert.equal(preflight.observation.origin, "");
  }

  const firstAction = await engine.execute(
    request(identity, "2", "screenshot", "semantic"),
  );
  assert.equal(firstAction.status, "ok");
  if (firstAction.status === "ok") {
    assert.equal(firstAction.observation.origin, "https://example.com");
  }
  assert.equal(
    context.createdPages.at(-1)?.url(),
    "https://example.com/saved",
  );
  assert.equal(
    await continuation.lookup(identity),
    "https://example.com/saved",
    "preflight must not replace the saved continuation with about:blank",
  );
});

test("history traversal supports BFCache without classifying a loading document", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);

  for (const [index, kind] of (["back", "forward"] as const).entries()) {
    const response = await engine.execute(
      request(identity, String(index + 1), kind),
    );
    assert.equal(response.status, "ok");
  }
  const page = context.createdPages[0];
  assert.ok(page);
  assert.deepEqual(page.historyWaitUntil, ["commit", "commit"]);
  assert.equal(page.documentReadyWaits, 2);
});

test("executes a safe batch with exactly one final observation", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);
  const response = await engine.execute({
    ...request(fixtureIdentity(1), "1", "screenshot"),
    action: {
      kind: "batch",
      observation: "screenshot",
      actions: [{ kind: "screenshot" }, { kind: "screenshot" }],
    },
  });
  assert.equal(response.status, "ok");
  assert.equal(context.createdPages[0]?.screenshotCalls, 1);
});

test("reports the failed action index for any batch execution failure", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.waitError = new Error("fixture wait failure");
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const response = await engine.execute({
    ...request(fixtureIdentity(1), "1", "screenshot"),
    action: {
      kind: "batch",
      actions: [
        { kind: "screenshot" },
        { kind: "wait", duration_ms: 1 },
        { kind: "screenshot" },
      ],
    },
  });
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_RUNTIME_UNAVAILABLE");
    assert.equal(response.error.recoverable, true);
    assert.equal(response.error.action_index, 1);
  }
});

test("keeps 403 active and locally blocks only the fourth navigation", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    page.responseStatus = 403;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 10,
    maxNavigationsPerMinute: 4,
    now: () => 1_000_000,
  });
  await budget.load();
  (
    engine as unknown as {
      originBudget: OriginBudgetStore | undefined;
    }
  ).originBudget = budget;
  for (let attempt = 1; attempt <= 4; attempt++) {
    const response = await engine.execute(
      request(identity, String(attempt), "navigate"),
    );
    assert.equal(response.status, "error");
    if (response.status === "error") {
      assert.equal(response.error.code, "BROWSER_ACCESS_DENIED");
      assert.equal(response.error.consecutive_access_denials, Math.min(3, attempt));
      assert.equal(
        response.error.origin_blocked_for_attachment,
        attempt >= 3 ? true : undefined,
      );
    }
  }
  assert.equal(page?.publicNavigationCalls, 3);

  const historyBack = await engine.execute(
    request(identity, "history-back", "back"),
  );
  assert.equal(
    historyBack.status,
    "ok",
    "history traversal must not guess the blocked target origin",
  );
  assert.deepEqual(page?.historyWaitUntil, ["commit"]);

  const historyForward = await engine.execute(
    request(identity, "history-forward", "forward"),
  );
  assert.equal(historyForward.status, "error");
  if (historyForward.status === "error") {
    assert.equal(
      historyForward.error.code,
      "BROWSER_ORIGIN_RATE_LIMITED",
      "history traversal must charge navigation budget to the active origin",
    );
  }
});

test("distinguishes suspected and required challenge fixtures", async () => {
  for (const fixture of [
    {
      selector: '[id*="captcha" i]',
      code: "BROWSER_CHALLENGE_SUSPECTED",
      recoverable: true,
    },
    {
      selector:
        'iframe[src^="https://challenges.cloudflare.com/turnstile/"]',
      code: "BROWSER_CHALLENGE_REQUIRED",
      recoverable: false,
    },
  ] as const) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const context = new FakeContext(() => {
      const page = new FakePage();
      page.challengeSelectors.add(fixture.selector);
      return page;
    });
    const engine = createEngine(context, continuation, async () => true);
    const response = await engine.execute(
      request(fixtureIdentity(1), "1", "navigate"),
    );
    assert.equal(response.status, "error");
    if (response.status === "error") {
      assert.equal(response.error.code, fixture.code);
      assert.equal(response.error.recoverable, fixture.recoverable);
      assert.equal(
        response.error.classifier_rules_version,
        "openlinker.browser.challenge-rules.v1",
      );
    }
  }
});

test("keeps suspected state sticky across unrelated errors after CDP loss", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.challengeSelectors.add('[id*="captcha" i]');
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);
  const suspected = await engine.execute(request(identity, "1", "navigate"));
  assert.equal(suspected.status, "error");
  if (suspected.status === "error") {
    assert.equal(suspected.error.code, "BROWSER_CHALLENGE_SUSPECTED");
  }

  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 1,
    maxNavigationsPerMinute: 1,
    now: () => 1_000_000,
  });
  await budget.load();
  assert.equal(
    budget.chargeAction(identity, "https://public.example"),
    undefined,
  );
  const internal = engine as unknown as {
    documentTracker: {
      generation: number;
      healthy: boolean;
      detach: () => Promise<void>;
    };
    originBudget: OriginBudgetStore | undefined;
  };
  internal.documentTracker = {
    generation: 1,
    healthy: false,
    detach: async () => undefined,
  };
  internal.originBudget = budget;

  const rateLimited = await engine.execute(
    request(identity, "2", "screenshot"),
  );
  assert.equal(rateLimited.status, "error");
  if (rateLimited.status === "error") {
    assert.equal(rateLimited.error.code, "BROWSER_ORIGIN_RATE_LIMITED");
    assert.equal(rateLimited.error.challenge_release_unavailable, true);
  }

  internal.originBudget = undefined;
  internal.documentTracker.healthy = true;
  internal.documentTracker.generation = 2;
  const reconnected = await engine.execute(
    request(identity, "3", "screenshot"),
  );
  assert.equal(reconnected.status, "error");
  if (reconnected.status === "error") {
    assert.equal(
      reconnected.error.code,
      "BROWSER_CHALLENGE_SUSPECTED",
    );
    assert.equal(
      reconnected.error.challenge_release_unavailable,
      true,
      "a healthy replacement tracker must not clear attachment-sticky state",
    );
  }

  context.createdPages[0]?.challengeSelectors.clear();
  const newAttachment = {
    ...identity,
    attachment_id: "55555555-5555-4555-8555-555555555555",
    control_epoch: 2,
  };
  const recovered = await engine.execute(
    request(newAttachment, "4", "screenshot"),
  );
  assert.equal(recovered.status, "ok");
  if (recovered.status === "ok") {
    assert.equal(
      recovered.observation.challenge_release_unavailable,
      undefined,
    );
  }
});

test("fails closed when the document tracker factory is missing", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  type UnsafeConstructor = new (
    context: BrowserContext,
    page: Page,
    continuation: PageContinuationStore,
    gatewayHealthProbe: GatewayHealthProbe,
    environment: undefined,
    originBudget: undefined,
    documentTrackerFactory: undefined,
    controlState: { controller: "agent"; attachmentKey: string },
  ) => BrowserEngine;
  const Constructor = BrowserEngine as unknown as UnsafeConstructor;
  const engine = new Constructor(
    context as unknown as BrowserContext,
    context.initialPage as unknown as Page,
    continuation,
    async () => true,
    undefined,
    undefined,
    undefined,
    { controller: "agent", attachmentKey: "" },
  );

  const response = await engine.execute(
    request(fixtureIdentity(1), "missing-tracker", "screenshot"),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_RUNTIME_UNAVAILABLE");
    assert.equal(response.error.recoverable, false);
  }
});

test("reclassifies trusted document changes but not same-document history", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.challengeSelectors.add('[id*="captcha" i]');
    return page;
  });
  const tracker = new FakeDocumentTracker();
  const engine = createEngine(
    context,
    continuation,
    async () => true,
    async () => tracker,
  );
  const identity = fixtureIdentity(1);

  const suspected = await engine.execute(request(identity, "1", "navigate"));
  assert.equal(suspected.status, "error");
  if (suspected.status === "error") {
    assert.equal(suspected.error.code, "BROWSER_CHALLENGE_SUSPECTED");
  }
  const page = context.createdPages[0];
  assert.ok(page);
  page.challengeSelectors.clear();

  const sameDocument = await engine.execute(
    request(identity, "2", "screenshot"),
  );
  assert.equal(sameDocument.status, "ok");
  assert.equal(
    (engine as unknown as { suspectedDocumentGeneration?: number })
      .suspectedDocumentGeneration,
    1,
    "pushState/same-document changes must not release the suspected gate",
  );

  tracker.generation = 2;
  const cleanDocument = await engine.execute(
    request(identity, "3", "screenshot"),
  );
  assert.equal(cleanDocument.status, "ok");
  assert.equal(
    (engine as unknown as { suspectedDocumentGeneration?: number })
      .suspectedDocumentGeneration,
    undefined,
  );

  page.challengeSelectors.add('[id*="captcha" i]');
  tracker.generation = 3;
  const restoredChallenge = await engine.execute(
    request(identity, "4", "screenshot"),
  );
  assert.equal(restoredChallenge.status, "error");
  if (restoredChallenge.status === "error") {
    assert.equal(
      restoredChallenge.error.code,
      "BROWSER_CHALLENGE_SUSPECTED",
    );
  }
});

test("focuses the pinned safe text handle without pointer activation", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const target = new FakeElementState(safeTextMetadata());
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    page.hitTestElement = target;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);

  const response = await engine.execute(
    actionRequest(fixtureIdentity(1), "focus-1", {
      kind: "click",
      x: 120,
      y: 80,
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "ok");
  if (response.status === "ok") {
    assert.equal(response.observation.click_effect, "focused");
    assert.equal(response.observation.target_category, "text_input");
  }
  assert.equal(target.focusCalls, 1);
  assert.equal(target.clickCalls, 0);
  assert.equal(page?.activeElement, target);
});

test("types through the pinned active handle after global focus moves", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const original = new FakeElementState(safeTextMetadata(), "hello", 5, 5);
  const moved = new FakeElementState(safeTextMetadata(), "untouched", 9, 9);
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    page.activeElement = original;
    original.onMetadata = () => {
      if (page !== undefined) {
        page.activeElement = moved;
      }
    };
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);

  const response = await engine.execute(
    actionRequest(fixtureIdentity(1), "type-1", {
      kind: "type_non_secret",
      text: " world",
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "ok");
  assert.equal(original.value, "hello world");
  assert.equal(original.fillCalls, 1);
  assert.equal(moved.value, "untouched");
  assert.equal(moved.fillCalls, 0);
});

test("rejects an oversized existing value without changing the field", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const originalValue = "x".repeat(16 * 1024 + 1);
  const target = new FakeElementState(
    safeTextMetadata(),
    originalValue,
    originalValue.length,
    originalValue.length,
  );
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.activeElement = target;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);

  const response = await engine.execute(
    actionRequest(fixtureIdentity(1), "type-overflow", {
      kind: "type_non_secret",
      text: "y",
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_ACTION_REJECTED");
    assert.equal(response.error.recoverable, false);
  }
  assert.equal(target.value, originalValue);
  assert.equal(target.fillCalls, 0);
});

test("blocked click exposes only a coarse target category", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const target = new FakeElementState({
    ...safeTextMetadata(),
    tagName: "button",
    type: "submit",
    label: "PAGE_CONTROLLED_SECRET_LABEL",
  });
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.hitTestElement = target;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);

  const response = await engine.execute(
    actionRequest(fixtureIdentity(1), "blocked-1", {
      kind: "click",
      x: 120,
      y: 80,
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_HIGH_IMPACT_ACTION_BLOCKED");
    assert.equal(response.error.target_category, "button");
    assert.equal(response.error.navigation_generation, 1);
    assert.ok(response.error.page_state_id);
  }
  assert.equal(
    JSON.stringify(response).includes("PAGE_CONTROLLED_SECRET_LABEL"),
    false,
  );
});

type GatewayHealthProbe = (timeout: number) => Promise<boolean>;

function createEngine(
  context: FakeContext,
  continuation: PageContinuationStore,
  gatewayHealthProbe: GatewayHealthProbe,
  documentTrackerFactory?: (
    page: Page,
  ) => Promise<FakeDocumentTracker>,
): BrowserEngine {
  type TestConstructor = new (
    context: BrowserContext,
    page: Page,
    continuation: PageContinuationStore,
    gatewayHealthProbe: GatewayHealthProbe,
    environment: undefined,
    originBudget: undefined,
    documentTrackerFactory: (
      page: Page,
    ) => Promise<FakeDocumentTracker>,
    controlState: { controller: "agent"; attachmentKey: string },
  ) => BrowserEngine;
  const Constructor = BrowserEngine as unknown as TestConstructor;
  return new Constructor(
    context as unknown as BrowserContext,
    context.initialPage as unknown as Page,
    continuation,
    gatewayHealthProbe,
    undefined,
    undefined,
    documentTrackerFactory ?? (async () => new FakeDocumentTracker()),
    { controller: "agent", attachmentKey: "" },
  );
}

class FakeDocumentTracker {
  generation = 1;
  healthy = true;

  async detach(): Promise<void> {
    this.healthy = false;
  }
}

class FakeContext {
  readonly initialPage = new FakePage();
  readonly createdPages: FakePage[] = [];
  private readonly createPage: () => FakePage;

  constructor(createPage: () => FakePage) {
    this.createPage = createPage;
  }

  pages(): Page[] {
    return [this.initialPage, ...this.createdPages]
      .filter((page) => !page.isClosed()) as unknown as Page[];
  }

  async newPage(): Promise<Page> {
    const page = this.createPage();
    this.createdPages.push(page);
    return page as unknown as Page;
  }
}

class FakePage {
  readonly sessionStorage = new Map<string, string>();
  screenshotCalls = 0;
  ariaError: Error | undefined;
  bodyError: Error | undefined;
  waitError: Error | undefined;
  responseStatus = 200;
  retryAfter: string | undefined;
  publicNavigationCalls = 0;
  readonly historyWaitUntil: string[] = [];
  documentReadyWaits = 0;
  readonly challengeSelectors = new Set<string>();
  hitTestElement: FakeElementState | undefined;
  activeElement: FakeElementState | undefined;
  private currentURL = "about:blank";
  private closed = false;
  private readonly frame = {};
  private readonly listeners = new Map<string, Array<(value: any) => void>>();
  private readonly navigate: (url: string) => Promise<void>;

  constructor(navigate: (url: string) => Promise<void> = async () => undefined) {
    this.navigate = navigate;
  }

  on(event: string, listener: (value: any) => void): this {
    const listeners = this.listeners.get(event) ?? [];
    listeners.push(listener);
    this.listeners.set(event, listeners);
    return this;
  }

  isClosed(): boolean {
    return this.closed;
  }

  async close(): Promise<void> {
    this.closed = true;
  }

  setDefaultTimeout(): void {}

  setDefaultNavigationTimeout(): void {}

  async goto(url: string): Promise<null> {
    await this.navigate(url);
    this.currentURL = url;
    if (url.startsWith("http://") || url.startsWith("https://")) {
      this.publicNavigationCalls++;
      const headers =
        this.retryAfter === undefined
          ? {}
          : { "retry-after": this.retryAfter };
      for (const listener of this.listeners.get("response") ?? []) {
        listener({
          request: () => ({
            isNavigationRequest: () => true,
          }),
          frame: () => this.frame,
          headers: () => headers,
          status: () => this.responseStatus,
          url: () => url,
        });
      }
    }
    return null;
  }

  url(): string {
    return this.currentURL;
  }

  mainFrame(): object {
    return this.frame;
  }

  async screenshot(): Promise<Buffer> {
    this.screenshotCalls++;
    return Buffer.from(`fixture screenshot ${this.screenshotCalls}`);
  }

  async waitForTimeout(): Promise<void> {
    if (this.waitError !== undefined) {
      throw this.waitError;
    }
  }

  async goBack(options: { waitUntil?: string }): Promise<null> {
    this.historyWaitUntil.push(options.waitUntil ?? "");
    return null;
  }

  async goForward(options: { waitUntil?: string }): Promise<null> {
    this.historyWaitUntil.push(options.waitUntil ?? "");
    return null;
  }

  async waitForFunction(): Promise<void> {
    this.documentReadyWaits++;
  }

  async evaluateHandle(
    callback: (...args: any[]) => unknown,
  ): Promise<FakeJSHandle> {
    const source = String(callback);
    const target = source.includes("elementFromPoint")
      ? this.hitTestElement
      : this.activeElement;
    return new FakeJSHandle(target, (focused) => {
      this.activeElement = focused;
    });
  }

  locator(selector: string): {
    ariaSnapshot: () => Promise<string>;
    innerText: () => Promise<string>;
    count: () => Promise<number>;
  } {
    return {
      ariaSnapshot: async () => {
        if (this.ariaError !== undefined) {
          throw this.ariaError;
        }
        return "";
      },
      innerText: async () => {
        if (this.bodyError !== undefined) {
          throw this.bodyError;
        }
        return "";
      },
      count: async () => (this.challengeSelectors.has(selector) ? 1 : 0),
    };
  }

  async title(): Promise<string> {
    return this.currentURL === "about:blank" ? "" : "Fixture page";
  }
}

interface FakeInteractiveMetadata {
  tagName: string;
  type: string;
  autocomplete: string;
  label: string;
  href: string;
  role: string;
  name: string;
  placeholder: string;
  formMethod: string;
  formAction: string;
  formRole: string;
  download: boolean;
}

class FakeElementState {
  focusCalls = 0;
  clickCalls = 0;
  fillCalls = 0;
  onMetadata: (() => void) | undefined;

  constructor(
    readonly metadata: FakeInteractiveMetadata,
    public value = "",
    public selectionStart: number | null = value.length,
    public selectionEnd: number | null = value.length,
  ) {}
}

class FakeJSHandle {
  private readonly element: FakeElementHandle | undefined;

  constructor(
    target: FakeElementState | undefined,
    onFocus: (target: FakeElementState) => void,
  ) {
    this.element =
      target === undefined ? undefined : new FakeElementHandle(target, onFocus);
  }

  asElement(): FakeElementHandle | null {
    return this.element ?? null;
  }

  async dispose(): Promise<void> {}
}

class FakeElementHandle {
  private evaluationCount = 0;

  constructor(
    private readonly target: FakeElementState,
    private readonly onFocus: (target: FakeElementState) => void,
  ) {}

  async evaluate(): Promise<unknown> {
    this.evaluationCount++;
    if (this.evaluationCount === 1) {
      this.target.onMetadata?.();
      return this.target.metadata;
    }
    return {
      value: this.target.value,
      selectionStart: this.target.selectionStart,
      selectionEnd: this.target.selectionEnd,
    };
  }

  async focus(): Promise<void> {
    this.target.focusCalls++;
    this.onFocus(this.target);
  }

  async click(): Promise<void> {
    this.target.clickCalls++;
  }

  async fill(value: string): Promise<void> {
    this.target.fillCalls++;
    this.target.value = value;
  }

  async dispose(): Promise<void> {}
}

function safeTextMetadata(): FakeInteractiveMetadata {
  return {
    tagName: "input",
    type: "search",
    autocomplete: "off",
    label: "Public search",
    href: "",
    role: "",
    name: "q",
    placeholder: "",
    formMethod: "get",
    formAction: "https://public.example/search",
    formRole: "search",
    download: false,
  };
}

function request(
  identity: Identity,
  actionID: string,
  kind: "navigate" | "screenshot" | "preflight" | "back" | "forward",
  observation?: ObservationMode,
): EngineRequest {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    deadline: new Date(Date.now() + 30_000).toISOString(),
    identity,
    action:
      kind === "navigate"
        ? {
            kind,
            url: "https://public.example/redirect",
            ...(observation === undefined ? {} : { observation }),
          }
        : {
            kind,
            ...(observation === undefined ? {} : { observation }),
          },
  };
}

function actionRequest(
  identity: Identity,
  actionID: string,
  action: BrowserAction,
): EngineRequest {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    deadline: new Date(Date.now() + 30_000).toISOString(),
    identity,
    action,
  };
}

function fixtureIdentity(index: number): Identity {
  const suffix = index.toString(16).padStart(12, "0");
  return {
    run_id: "11111111-1111-4111-8111-111111111111",
    agent_id: "22222222-2222-4222-8222-222222222222",
    principal_scope_id: "ps1_fixture_scope",
    browser_session_id: `33333333-3333-4333-8333-${suffix}`,
    session_epoch: 1,
    attachment_id: "44444444-4444-4444-8444-444444444444",
    control_epoch: 1,
    controller: "agent",
  };
}
