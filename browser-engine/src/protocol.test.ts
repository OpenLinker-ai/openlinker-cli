import assert from "node:assert/strict";
import test from "node:test";

import {
  ENGINE_CONTRACT_ID,
  ENGINE_VIEWER_CONTRACT_ID,
  failure,
  parseRequest,
  parseViewerRequest,
  truncateUTF8,
} from "./protocol.js";

function request(): Record<string, unknown> {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: "1",
    deadline: "2030-01-01T00:00:00.000000000Z",
    identity: {
      run_id: "11111111-1111-4111-8111-111111111111",
      agent_id: "22222222-2222-4222-8222-222222222222",
      principal_scope_id: "scope_333333333333",
      browser_session_id: "44444444-4444-4444-8444-444444444444",
      session_epoch: 1,
      attachment_id: "55555555-5555-4555-8555-555555555555",
      control_epoch: 1,
      controller: "agent",
    },
    action: {
      kind: "navigate",
      url: "https://example.com/path",
    },
  };
}

test("strictly parses the closed engine request", () => {
  const parsed = parseRequest(
    JSON.stringify(request()),
    Date.parse("2029-12-31T23:59:30Z"),
  );
  assert.equal(parsed.action.kind, "navigate");
  assert.equal(parsed.action.url, "https://example.com/path");
  assert.equal(parsed.action.observation, undefined);
});

test("strictly separates human Viewer input from Agent actions", () => {
  const value = request();
  const identity = value.identity as Record<string, unknown>;
  identity.controller = "human";
  const viewer = {
    contract_id: ENGINE_VIEWER_CONTRACT_ID,
    action_id: "2",
    deadline: value.deadline,
    identity,
    operation: "input",
    input: {
      kind: "pointer",
      pointer_action: "click",
      x: 100,
      y: 200,
      button: "left",
      click_count: 1,
    },
  };
  assert.equal(
    parseViewerRequest(
      JSON.stringify(viewer),
      Date.parse("2029-12-31T23:59:30Z"),
    ).operation,
    "input",
  );
  assert.throws(() =>
    parseRequest(
      JSON.stringify({ ...value, identity }),
      Date.parse("2029-12-31T23:59:30Z"),
    ),
  );
  assert.throws(() =>
    parseViewerRequest(
      JSON.stringify({
        ...viewer,
        input: { ...viewer.input, javascript: "document.cookie" },
      }),
      Date.parse("2029-12-31T23:59:30Z"),
    ),
  );
});

test("accepts internal observation modes and rejects unknown modes", () => {
  for (const observation of ["semantic", "screenshot", "both", "none"]) {
    const value = request();
    (value.action as Record<string, unknown>).observation = observation;
    assert.equal(
      parseRequest(
        JSON.stringify(value),
        Date.parse("2029-12-31T23:59:30Z"),
      ).action.observation,
      observation,
    );
  }
  const value = request();
  (value.action as Record<string, unknown>).observation = "verbose";
  assert.throws(() =>
    parseRequest(JSON.stringify(value), Date.parse("2029-12-31T23:59:30Z")),
  );
});

test("rejects unknown fields, credentials, and non-public URL shapes", () => {
  for (const mutate of [
    (value: Record<string, unknown>) => {
      value.provider_api_key = "secret";
    },
    (value: Record<string, unknown>) => {
      (value.action as Record<string, unknown>).javascript = "alert(1)";
    },
    (value: Record<string, unknown>) => {
      (value.action as Record<string, unknown>).url = "http://127.0.0.1/";
    },
  ]) {
    const value = request();
    mutate(value);
    assert.throws(() =>
      parseRequest(JSON.stringify(value), Date.parse("2029-12-31T23:59:30Z")),
    );
  }
});

test("rejects alternate loopback encodings before Chromium", () => {
  for (const rawURL of [
    "http://2130706433/",
    "http://0177.0.0.1/",
    "http://0x7f000001/",
    "http://[::1]/",
    "http://localhost/",
    "http://metadata.google.internal/",
  ]) {
    const value = request();
    (value.action as Record<string, unknown>).url = rawURL;
    assert.throws(() =>
      parseRequest(JSON.stringify(value), Date.parse("2029-12-31T23:59:30Z")),
    );
  }
});

test("uses the fixed viewport and rejects disallowed Phase 1 controls", () => {
  for (const [x, y, accepted] of [
    [0, 0, true],
    [1279, 719, true],
    [1280, 0, false],
    [0, 720, false],
  ] as const) {
    const value = request();
    value.action = { kind: "click", x, y };
    const parse = () =>
      parseRequest(
        JSON.stringify(value),
        Date.parse("2029-12-31T23:59:30Z"),
      );
    if (accepted) {
      assert.doesNotThrow(parse);
    } else {
      assert.throws(parse);
    }
  }
  for (const action of [
    { kind: "keypress", key: "Space" },
    { kind: "select", value: "option-1" },
  ]) {
    const value = request();
    value.action = action;
    assert.throws(() =>
      parseRequest(JSON.stringify(value), Date.parse("2029-12-31T23:59:30Z")),
    );
  }
});

test("accepts only bounded observation-free safe batches", () => {
  const accepted = request();
  accepted.action = {
    kind: "batch",
    observation: "both",
    actions: [
      { kind: "scroll", delta_y: 100 },
      { kind: "wait", duration_ms: 25 },
      { kind: "screenshot" },
    ],
  };
  const parsed = parseRequest(
    JSON.stringify(accepted),
    Date.parse("2029-12-31T23:59:30Z"),
  );
  assert.equal(parsed.action.kind, "batch");
  assert.equal(parsed.action.actions?.length, 3);

  for (const actions of [
    [{ kind: "wait", duration_ms: 1 }],
    Array.from({ length: 9 }, () => ({ kind: "screenshot" })),
    [{ kind: "click", x: 1, y: 1 }, { kind: "screenshot" }],
    [
      { kind: "wait", duration_ms: 1, observation: "semantic" },
      { kind: "screenshot" },
    ],
    [
      {
        kind: "batch",
        actions: [{ kind: "screenshot" }, { kind: "screenshot" }],
      },
      { kind: "screenshot" },
    ],
  ]) {
    const rejected = request();
    rejected.action = { kind: "batch", actions };
    assert.throws(() =>
      parseRequest(
        JSON.stringify(rejected),
        Date.parse("2029-12-31T23:59:30Z"),
      ),
    );
  }
});

test("bounds UTF-8 error messages without splitting code points", () => {
  const message = "浏览器".repeat(300);
  const bounded = truncateUTF8(message, 500);
  assert.ok(Buffer.byteLength(bounded, "utf8") <= 500);
  assert.doesNotThrow(() => Buffer.from(bounded, "utf8").toString("utf8"));
  assert.ok(Buffer.byteLength(failure("1", "BROWSER_INTERNAL", message, false).error.message) <= 500);
  assert.equal(
    failure("1", "BROWSER_RUNTIME_UNAVAILABLE", "failed", true, 2).error
      .action_index,
    2,
  );
});

test("preserves bounded site outcome evidence", () => {
  const response = failure(
    "1",
    "BROWSER_CHALLENGE_SUSPECTED",
    "interactive challenge may be present",
    true,
    undefined,
    {
      site_outcome: "BROWSER_CHALLENGE_SUSPECTED",
      classifier_rules_version: "openlinker.browser.challenge-rules.v1",
      challenge_release_unavailable: true,
    },
  );
  assert.deepEqual(response.error, {
    code: "BROWSER_CHALLENGE_SUSPECTED",
    message: "interactive challenge may be present",
    recoverable: true,
    site_outcome: "BROWSER_CHALLENGE_SUSPECTED",
    classifier_rules_version: "openlinker.browser.challenge-rules.v1",
    challenge_release_unavailable: true,
  });
});
