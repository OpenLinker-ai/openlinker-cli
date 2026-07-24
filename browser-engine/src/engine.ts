import { createHash } from "node:crypto";
import path from "node:path";

import {
  chromium,
  type BrowserContext,
  type Page,
} from "playwright-core";

import {
  failure,
  type BrowserAction,
  type BrowserErrorCode,
  type EngineRequest,
  type EngineResponse,
  type Observation,
  success,
  truncateUTF8,
} from "./protocol.js";
import {
  blocksHighImpactActivation,
  blocksNonSecretTyping,
  type InteractiveMetadata,
} from "./policy.js";

const MAX_SCREENSHOT_BYTES = 4 * 1024 * 1024;
const MAX_STATE_TEXT_BYTES = 256 * 1024;
const MAX_PAGES = 4;
const VIEWPORT = { width: 1280, height: 720 };

const CHROMIUM_FLAGS = [
  "--disable-background-networking",
  "--disable-component-update",
  "--disable-default-apps",
  "--disable-domain-reliability",
  "--disable-features=DnsOverHttpsUpgrade",
  "--disable-quic",
  "--disable-sync",
  "--dns-over-https-mode=off",
  "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
  "--metrics-recording-only",
  "--no-first-run",
  "--no-pings",
  "--webrtc-ip-handling-policy=disable_non_proxied_udp",
] as const;

export class BrowserEngine {
  private readonly context: BrowserContext;
  private page: Page;
  private observationSequence = 0;
  private lastBodyText = "";

  private constructor(context: BrowserContext, page: Page) {
    this.context = context;
    this.page = page;
  }

  static async create(environment: NodeJS.ProcessEnv): Promise<BrowserEngine> {
    const proxy = requireProxy(environment.OPENLINKER_BROWSER_EGRESS_PROXY);
    const profileDirectory = requireProfileDirectory(
      environment.OPENLINKER_BROWSER_PROFILE_DIR,
    );
    const context = await chromium.launchPersistentContext(profileDirectory, {
      acceptDownloads: false,
      args: [...CHROMIUM_FLAGS],
      headless: true,
      proxy: { server: proxy },
      serviceWorkers: "allow",
      viewport: VIEWPORT,
    });
    await context.clearPermissions();
    const pages = context.pages();
    const page = pages[0] ?? (await context.newPage());
    const engine = new BrowserEngine(context, page);
    for (const existingPage of context.pages()) {
      engine.configurePage(existingPage);
    }
    context.on("page", (newPage) => {
      engine.configurePage(newPage);
      if (context.pages().length > MAX_PAGES) {
        void newPage.close();
        return;
      }
      engine.page = newPage;
    });
    return engine;
  }

  async execute(request: EngineRequest): Promise<EngineResponse> {
    try {
      const timeout = remainingTimeout(request.deadline);
      await this.executeAction(request.action, timeout);
      return success(request.action_id, await this.observe(timeout));
    } catch (error) {
      if (error instanceof EngineActionError) {
        return failure(
          request.action_id,
          error.code,
          error.message,
          error.recoverable,
        );
      }
      const message = error instanceof Error ? error.message : "browser action failed";
      if (Date.parse(request.deadline) <= Date.now()) {
        return failure(
          request.action_id,
          "BROWSER_CANCELED",
          "browser action deadline elapsed",
          true,
        );
      }
      return failure(
        request.action_id,
        request.action.kind === "navigate"
          ? "BROWSER_EGRESS_UNAVAILABLE"
          : "BROWSER_RUNTIME_UNAVAILABLE",
        safeRuntimeMessage(message),
        true,
      );
    }
  }

  async close(): Promise<void> {
    await this.context.close();
  }

  private configurePage(page: Page): void {
    page.on("dialog", (dialog) => {
      void dialog.dismiss();
    });
    page.on("download", (download) => {
      void download.cancel();
    });
  }

  private async executeAction(action: BrowserAction, timeout: number): Promise<void> {
    const page = this.page;
    page.setDefaultTimeout(timeout);
    page.setDefaultNavigationTimeout(timeout);
    switch (action.kind) {
      case "navigate":
        await page.goto(action.url ?? "", {
          timeout,
          waitUntil: "domcontentloaded",
        });
        return;
      case "click": {
        const x = action.x ?? -1;
        const y = action.y ?? -1;
        assertViewportPoint(x, y);
        const metadata = await interactiveMetadataAt(page, x, y);
        if (blocksHighImpactActivation(metadata)) {
          throw new EngineActionError(
            "BROWSER_HIGH_IMPACT_ACTION_BLOCKED",
            "Phase 1 blocks this high-impact browser action",
            false,
          );
        }
        await page.mouse.click(x, y);
        return;
      }
      case "type_non_secret": {
        const metadata = await activeInteractiveMetadata(page);
        if (blocksNonSecretTyping(metadata)) {
          throw new EngineActionError(
            "BROWSER_USER_ACTION_REQUIRED",
            "credential fields require a later human-control phase",
            false,
          );
        }
        await page.keyboard.insertText(action.text ?? "");
        return;
      }
      case "scroll":
        await page.mouse.wheel(action.delta_x ?? 0, action.delta_y ?? 0);
        return;
      case "keypress":
        if (
          action.key === "Enter" &&
          blocksHighImpactActivation(await activeInteractiveMetadata(page))
        ) {
          throw new EngineActionError(
            "BROWSER_HIGH_IMPACT_ACTION_BLOCKED",
            "Phase 1 blocks this high-impact browser action",
            false,
          );
        }
        await page.keyboard.press(action.key === "Space" ? " " : (action.key ?? ""));
        return;
      case "select": {
        const x = action.x ?? -1;
        const y = action.y ?? -1;
        assertViewportPoint(x, y);
        const selected = await page.evaluate(
          ({ pointX, pointY, value }) => {
            const element = document.elementFromPoint(pointX, pointY);
            const select =
              element instanceof HTMLSelectElement
                ? element
                : element?.closest("select");
            if (!(select instanceof HTMLSelectElement)) {
              return false;
            }
            if (![...select.options].some((option) => option.value === value)) {
              return false;
            }
            select.value = value;
            select.dispatchEvent(new Event("input", { bubbles: true }));
            select.dispatchEvent(new Event("change", { bubbles: true }));
            return true;
          },
          { pointX: x, pointY: y, value: action.value ?? "" },
        );
        if (!selected) {
          throw new EngineActionError(
            "BROWSER_USER_ACTION_REQUIRED",
            "select target or value is unavailable",
            false,
          );
        }
        return;
      }
      case "wait":
        await page.waitForTimeout(action.duration_ms ?? 1);
        return;
      case "back":
        await page.goBack({ timeout, waitUntil: "domcontentloaded" });
        return;
      case "forward":
        await page.goForward({ timeout, waitUntil: "domcontentloaded" });
        return;
      case "screenshot":
        return;
    }
  }

  private async observe(timeout: number): Promise<Observation> {
    const page = this.page;
    const screenshot = await page.screenshot({
      animations: "disabled",
      caret: "hide",
      quality: 65,
      timeout,
      type: "jpeg",
    });
    if (screenshot.byteLength > MAX_SCREENSHOT_BYTES) {
      throw new EngineActionError(
        "BROWSER_OUTPUT_TOO_LARGE",
        "browser screenshot exceeds the output limit",
        false,
      );
    }
    const body = page.locator("body");
    const [title, ariaSnapshot, bodyText] = await Promise.all([
      page.title(),
      body
        .ariaSnapshot({ timeout: Math.min(timeout, 2000) })
        .catch(() => ""),
      body
        .innerText({ timeout: Math.min(timeout, 2000) })
        .catch(() => ""),
    ]);
    const boundedBody = truncateUTF8(bodyText, MAX_STATE_TEXT_BYTES);
    const domDiff = textDiff(this.lastBodyText, boundedBody);
    this.lastBodyText = boundedBody;
    this.observationSequence++;
    const pageStateID = createHash("sha256")
      .update(String(this.observationSequence))
      .update(screenshot)
      .digest("hex");
    return {
      page_state_id: pageStateID,
      screenshot: {
        mime_type: "image/jpeg",
        data: screenshot.toString("base64"),
      },
      ax_tree: {
        aria_snapshot: truncateUTF8(ariaSnapshot, MAX_STATE_TEXT_BYTES),
      },
      dom_diff: domDiff,
      origin: safeOrigin(page.url()),
      title: truncateUTF8(title, 2048),
    };
  }
}

class EngineActionError extends Error {
  readonly code: BrowserErrorCode;
  readonly recoverable: boolean;

  constructor(code: BrowserErrorCode, message: string, recoverable: boolean) {
    super(message);
    this.code = code;
    this.recoverable = recoverable;
  }
}

function requireProxy(raw: string | undefined): string {
  if (raw === undefined || raw.trim() !== raw) {
    throw new Error("OPENLINKER_BROWSER_EGRESS_PROXY is required");
  }
  const url = new URL(raw);
  if (
    url.protocol !== "http:" ||
    url.username !== "" ||
    url.password !== "" ||
    url.pathname !== "/" ||
    url.search !== "" ||
    url.hash !== ""
  ) {
    throw new Error("Browser egress proxy must be an HTTP origin without credentials");
  }
  return url.origin;
}

function requireProfileDirectory(raw: string | undefined): string {
  if (
    raw === undefined ||
    raw.trim() !== raw ||
    !path.isAbsolute(raw) ||
    path.normalize(raw) !== raw
  ) {
    throw new Error("OPENLINKER_BROWSER_PROFILE_DIR must be an absolute clean path");
  }
  return raw;
}

function remainingTimeout(deadline: string): number {
  const remaining = Date.parse(deadline) - Date.now();
  if (!Number.isFinite(remaining) || remaining <= 0) {
    throw new EngineActionError(
      "BROWSER_CANCELED",
      "browser action deadline elapsed",
      true,
    );
  }
  return Math.max(1, Math.min(remaining, 60_000));
}

function assertViewportPoint(x: number, y: number): void {
  if (x < 0 || y < 0 || x >= VIEWPORT.width || y >= VIEWPORT.height) {
    throw new EngineActionError(
      "BROWSER_OUTPUT_INVALID",
      "browser action coordinates are outside the viewport",
      false,
    );
  }
}

async function interactiveMetadataAt(
  page: Page,
  x: number,
  y: number,
): Promise<InteractiveMetadata> {
  return page.evaluate(
    ({ pointX, pointY }) => {
      const element = document.elementFromPoint(pointX, pointY);
      const interactive = element?.closest(
        "a,button,input,select,textarea,[role=button],[role=link]",
      );
      if (!(interactive instanceof HTMLElement)) {
        return {
          tagName: "",
          type: "",
          autocomplete: "",
          label: "",
          href: "",
        };
      }
      const input = interactive instanceof HTMLInputElement ? interactive : undefined;
      const anchor = interactive instanceof HTMLAnchorElement ? interactive : undefined;
      return {
        tagName: interactive.tagName.toLowerCase(),
        type: input?.type ?? "",
        autocomplete: input?.autocomplete ?? "",
        label: (
          interactive.getAttribute("aria-label") ??
          input?.value ??
          interactive.innerText ??
          ""
        ).slice(0, 512),
        href: (anchor?.href ?? "").slice(0, 1024),
      };
    },
    { pointX: x, pointY: y },
  );
}

async function activeInteractiveMetadata(page: Page): Promise<InteractiveMetadata> {
  return page.evaluate(() => {
    const active = document.activeElement;
    if (!(active instanceof HTMLElement)) {
      return {
        tagName: "",
        type: "",
        autocomplete: "",
        label: "",
        href: "",
      };
    }
    const input = active instanceof HTMLInputElement ? active : undefined;
    const anchor = active instanceof HTMLAnchorElement ? active : undefined;
    return {
      tagName: active.tagName.toLowerCase(),
      type: input?.type ?? "",
      autocomplete: input?.autocomplete ?? "",
      label: (
        active.getAttribute("aria-label") ??
        input?.value ??
        active.innerText ??
        ""
      ).slice(0, 512),
      href: (anchor?.href ?? "").slice(0, 1024),
    };
  });
}

function textDiff(
  previous: string,
  current: string,
): {
  kind: "baseline" | "diff";
  common_prefix: number;
  removed_characters: number;
  inserted: string;
  truncated: boolean;
} {
  if (previous === "") {
    return {
      kind: "baseline",
      common_prefix: 0,
      removed_characters: 0,
      inserted: current,
      truncated: Buffer.byteLength(current, "utf8") >= MAX_STATE_TEXT_BYTES,
    };
  }
  let commonPrefix = 0;
  const maximumPrefix = Math.min(previous.length, current.length);
  while (
    commonPrefix < maximumPrefix &&
    previous[commonPrefix] === current[commonPrefix]
  ) {
    commonPrefix++;
  }
  let commonSuffix = 0;
  while (
    commonSuffix < previous.length - commonPrefix &&
    commonSuffix < current.length - commonPrefix &&
    previous[previous.length - 1 - commonSuffix] ===
      current[current.length - 1 - commonSuffix]
  ) {
    commonSuffix++;
  }
  const inserted = current.slice(
    commonPrefix,
    commonSuffix === 0 ? current.length : current.length - commonSuffix,
  );
  return {
    kind: "diff",
    common_prefix: commonPrefix,
    removed_characters: previous.length - commonPrefix - commonSuffix,
    inserted: truncateUTF8(inserted, MAX_STATE_TEXT_BYTES),
    truncated: Buffer.byteLength(inserted, "utf8") > MAX_STATE_TEXT_BYTES,
  };
}

function safeOrigin(raw: string): string {
  try {
    const url = new URL(raw);
    if (url.protocol !== "http:" && url.protocol !== "https:") {
      return "";
    }
    return truncateUTF8(url.origin, 512);
  } catch {
    return "";
  }
}

function safeRuntimeMessage(message: string): string {
  const normalized = message.toLowerCase();
  if (normalized.includes("proxy") || normalized.includes("net::")) {
    return "Browser network path is unavailable";
  }
  return "Browser engine is unavailable";
}
