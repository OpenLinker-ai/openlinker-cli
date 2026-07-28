import { createHash } from "node:crypto";
import path from "node:path";

import {
  chromium,
  errors as playwrightErrors,
  type BrowserContext,
  type ElementHandle,
  type Page,
  type WebSocketRoute,
} from "playwright-core";

import {
  gatewayBlockedResponse,
  isTunnelConnectionFailure,
  probeEgressGateway,
} from "./egress-policy.js";
import {
  failure,
  type BrowserAction,
  type BrowserErrorCode,
  type ClickEffect,
  type EngineRequest,
  type EngineResponse,
  type EngineViewerRequest,
  type EngineViewerResponse,
  type EngineFailure,
  type EnvironmentEvidence,
  type Observation,
  type ObservationMode,
  type TargetCategory,
  type Controller,
  isPublicHTTPURL,
  success,
  truncateUTF8,
  viewerFailure,
  viewerSuccess,
} from "./protocol.js";
import {
  allowsPageRequestMethod,
  blocksHighImpactActivation,
  blocksKeypress,
  blocksNonSecretTyping,
  type InteractiveMetadata,
} from "./policy.js";
import {
  PageContinuationCorruptError,
  PageContinuationStore,
  pageContinuationSessionKey,
} from "./page-continuation.js";
import {
  environmentEvidence,
  parseBrowserEnvironment,
} from "./environment.js";
import {
  BROWSER_VIEWPORT_HEIGHT,
  BROWSER_VIEWPORT_WIDTH,
} from "./browser-contract.generated.js";
import { OriginBudgetStore } from "./origin-budget.js";
import {
  CLASSIFIER_RULES_VERSION,
  classifyChallenge,
  parseRetryAfter,
} from "./site-classifier.js";
import { DocumentGenerationTracker } from "./document-generation.js";

const MAX_SCREENSHOT_BYTES = 4 * 1024 * 1024;
const MAX_VIEWER_FRAME_BYTES = 1024 * 1024;
const MAX_STATE_TEXT_BYTES = 256 * 1024;
const MAX_TYPE_VALUE_BYTES = 16 * 1024;
const MAX_PAGES = 4;
const VIEWPORT = {
  width: BROWSER_VIEWPORT_WIDTH,
  height: BROWSER_VIEWPORT_HEIGHT,
};
type GatewayHealthProbe = (timeout: number) => Promise<boolean>;
type DocumentTrackerFactory = (
  page: Page,
) => Promise<DocumentGenerationTracker>;
export type ControlState = {
  controller: Controller;
  attachmentKey: string;
  pageWebSockets: Set<WebSocketRoute>;
};

const MAX_HUMAN_PAGE_WEBSOCKETS = 64;

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
  private readonly continuation: PageContinuationStore;
  private page: Page;
  private boundSessionKey = "";
  private boundAttachmentKey = "";
  private lastBodyText = "";
  private navigationGeneration = 1;
  private readonly blockedNavigationPages = new WeakSet<Page>();
  private readonly configuredPages = new WeakSet<Page>();
  private readonly gatewayHealthProbe: GatewayHealthProbe;
  private readonly environment: EnvironmentEvidence | undefined;
  private readonly originBudget: OriginBudgetStore | undefined;
  private readonly mainDocumentResponses = new WeakMap<
    Page,
    MainDocumentResponse
  >();
  private readonly consecutiveAccessDenials = new Map<string, number>();
  private readonly documentTrackerFactory: DocumentTrackerFactory;
  private documentTracker: DocumentGenerationTracker | undefined;
  private classifiedDocumentGeneration: number | undefined;
  private suspectedDocumentGeneration: number | undefined;
  private suspectedSticky = false;
  private readonly controlState: ControlState;

  private constructor(
    context: BrowserContext,
    page: Page,
    continuation: PageContinuationStore,
    gatewayHealthProbe: GatewayHealthProbe,
    environment: EnvironmentEvidence | undefined,
    originBudget: OriginBudgetStore | undefined,
    documentTrackerFactory: DocumentTrackerFactory,
    controlState: ControlState,
  ) {
    this.context = context;
    this.page = page;
    this.continuation = continuation;
    this.gatewayHealthProbe = gatewayHealthProbe;
    this.environment = environment;
    this.originBudget = originBudget;
    this.documentTrackerFactory = documentTrackerFactory;
    this.controlState = controlState;
  }

  static async create(environment: NodeJS.ProcessEnv): Promise<BrowserEngine> {
    const browserEnvironment = parseBrowserEnvironment(environment);
    const proxy = requireProxy(environment.OPENLINKER_BROWSER_EGRESS_PROXY);
    const profileDirectory = requireProfileDirectory(
      environment.OPENLINKER_BROWSER_PROFILE_DIR,
    );
    const context = await chromium.launchPersistentContext(profileDirectory, {
      acceptDownloads: false,
      args: [...CHROMIUM_FLAGS],
      channel: browserEnvironment.engine === "chrome" ? "chrome" : "chromium",
      headless: true,
      // Playwright disables BFCache by default to make request interception
      // deterministic. The Runtime's challenge-release contract needs real
      // BFCache restores, while the Egress Gateway remains the authoritative
      // network boundary for any navigation that does issue a request.
      ignoreDefaultArgs: ["--disable-back-forward-cache"],
      locale: browserEnvironment.locale,
      proxy: { server: proxy },
      serviceWorkers: "block",
      timezoneId: browserEnvironment.timezone,
      viewport: VIEWPORT,
    });
    const browserVersion = context.browser()?.version();
    if (browserVersion === undefined) {
      await context.close();
      throw new Error("Browser version is unavailable");
    }
    const controlState: ControlState = {
      controller: "agent",
      attachmentKey: "",
      pageWebSockets: new Set(),
    };
    await context.route("**/*", async (route) => {
      if (
        controlState.controller !== "human" &&
        !allowsPageRequestMethod(route.request().method())
      ) {
        await route.abort("blockedbyclient");
        return;
      }
      await route.continue();
    });
    await context.routeWebSocket("**/*", async (webSocket) => {
      await routePageWebSocket(controlState, webSocket);
    });
    await context.clearPermissions();
    const pages = context.pages();
    const page = pages[0] ?? (await context.newPage());
    const originBudget = new OriginBudgetStore({
      profileDirectory,
      profileGeneration: browserEnvironment.profileGeneration,
      maxActionsPerMinute: browserEnvironment.maxActionsPerOriginMinute,
      maxNavigationsPerMinute:
        browserEnvironment.maxNavigationsPerOriginMinute,
    });
    await originBudget.load();
    const engine = new BrowserEngine(
      context,
      page,
      new PageContinuationStore(profileDirectory),
      (timeout) => probeEgressGateway(proxy, timeout),
      environmentEvidence(browserEnvironment, browserVersion),
      originBudget,
      (trackedPage) => DocumentGenerationTracker.create(context, trackedPage),
      controlState,
    );
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
      await this.bindSession(request, timeout);
      this.refreshSuspectedSticky();
      this.admitOriginBudget(request);
      const effect = await this.executeAction(request.action, timeout, request);
      this.assertGatewayAllowed(this.page);
      assertAllowedPageURL(this.page.url());
      await this.classifyActionOutcome(request);
      const observation = await this.observe(
        request.action.observation ?? "semantic",
        timeout,
      );
      if (request.action.kind === "preflight") {
        if (this.environment !== undefined) {
          observation.environment = this.environment;
        }
      }
      if (effect !== undefined) {
        observation.click_effect = effect.clickEffect;
        observation.target_category = effect.targetCategory;
      }
      if (this.suspectedSticky) {
        observation.site_outcome = "BROWSER_CHALLENGE_SUSPECTED";
        observation.classifier_rules_version = CLASSIFIER_RULES_VERSION;
        observation.challenge_release_unavailable = true;
      }
      this.assertGatewayAllowed(this.page);
      assertAllowedPageURL(this.page.url());
      if (request.action.kind !== "preflight") {
        await this.continuation.record(
          request.identity,
          this.page.url(),
          Date.now(),
          request.action.kind === "checkpoint",
        );
      }
      if (request.action.kind === "checkpoint") {
        await this.originBudget?.checkpoint();
      }
      return success(request.action_id, observation);
    } catch (error) {
      if (error instanceof PageContinuationCorruptError) {
        return failure(
          request.action_id,
          "BROWSER_PROFILE_CORRUPT",
          "Browser Profile page state is invalid",
          false,
        );
      }
      if (error instanceof EngineActionError) {
        this.refreshSuspectedSticky();
        return failure(
          request.action_id,
          error.code,
          error.message,
          error.recoverable,
          error.actionIndex,
          this.suspectedSticky
            ? {
                ...error.details,
                challenge_release_unavailable: true,
              }
            : error.details,
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
      const tunnelFailure = await this.classifyTunnelFailure(
        error,
        Date.parse(request.deadline) - Date.now(),
      );
      if (tunnelFailure !== undefined) {
        return failure(
          request.action_id,
          tunnelFailure.code,
          tunnelFailure.message,
          tunnelFailure.recoverable,
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

  async executeViewer(
    request: EngineViewerRequest,
  ): Promise<EngineViewerResponse> {
    try {
      const timeout = remainingTimeout(request.deadline);
      const sessionKey = pageContinuationSessionKey(request.identity);
      const attachmentKey = viewerAttachmentKey(request.identity);
      if (request.operation === "enter") {
        if (
          this.boundSessionKey === "" ||
          this.boundSessionKey !== sessionKey ||
          this.controlState.controller === "human"
        ) {
          return viewerFailure(
            request.action_id,
            "BROWSER_ACTION_REJECTED",
            "browser viewer cannot claim this Session",
            false,
          );
        }
        this.boundAttachmentKey = attachmentKey;
        this.controlState.attachmentKey = attachmentKey;
        this.controlState.controller = "human";
        return viewerSuccess(request.action_id);
      }
      if (
        this.controlState.controller !== "human" ||
        this.controlState.attachmentKey !== attachmentKey ||
        this.boundSessionKey !== sessionKey
      ) {
        return viewerFailure(
          request.action_id,
          "BROWSER_ACTION_REJECTED",
          "browser viewer control is stale",
          false,
        );
      }
      if (request.operation === "exit") {
        // Reinstate the restrictive page policy before acknowledging release.
        await restoreAgentPagePolicy(this.controlState);
        return viewerSuccess(request.action_id);
      }
      if (request.operation === "frame") {
        const frame = await this.page.screenshot({
          animations: "disabled",
          caret: "initial",
          quality: 55,
          timeout,
          type: "jpeg",
        });
        if (frame.byteLength > MAX_VIEWER_FRAME_BYTES) {
          return viewerFailure(
            request.action_id,
            "BROWSER_OUTPUT_TOO_LARGE",
            "browser viewer frame exceeds the output limit",
            false,
          );
        }
        return viewerSuccess(request.action_id, {
          mime_type: "image/jpeg",
          data: frame.toString("base64"),
          width: BROWSER_VIEWPORT_WIDTH,
          height: BROWSER_VIEWPORT_HEIGHT,
        });
      }
      const input = request.input;
      if (input === undefined) {
        throw new Error("browser viewer input is missing");
      }
      switch (input.kind) {
        case "pointer":
          if (input.pointer_action === "move") {
            await this.page.mouse.move(input.x, input.y);
          } else {
            await this.page.mouse.click(input.x, input.y, {
              button: input.button ?? "left",
              clickCount: input.click_count ?? 1,
            });
          }
          break;
        case "keyboard":
          if (input.keyboard_action === "press") {
            await this.page.keyboard.press(input.key ?? "");
          } else {
            await this.page.keyboard.insertText(input.text ?? "");
          }
          break;
        case "scroll":
          await this.page.mouse.wheel(input.delta_x, input.delta_y);
          break;
      }
      return viewerSuccess(request.action_id);
    } catch (error) {
      return viewerFailure(
        request.action_id,
        Date.parse(request.deadline) <= Date.now()
          ? "BROWSER_CANCELED"
          : "BROWSER_RUNTIME_UNAVAILABLE",
        Date.parse(request.deadline) <= Date.now()
          ? "browser viewer deadline elapsed"
          : safeRuntimeMessage(
              error instanceof Error
                ? error.message
                : "browser viewer operation failed",
            ),
        true,
      );
    }
  }

  private admitOriginBudget(request: EngineRequest): void {
    if (
      request.action.kind === "preflight" ||
      request.action.kind === "checkpoint"
    ) {
      return;
    }
    const origin =
      request.action.kind === "navigate"
        ? safeOrigin(request.action.url ?? "")
        : safeOrigin(this.page.url());
    if (origin === "") {
      return;
    }
    if (this.originBudget !== undefined) {
      const actionCount =
        request.action.kind === "batch"
          ? (request.action.actions?.length ?? 1)
          : 1;
      const actionDelay = this.originBudget.chargeAction(
        request.identity,
        origin,
        actionCount,
      );
      if (actionDelay !== undefined) {
        throw originRateLimited(actionDelay);
      }
      const retryDelay = this.originBudget.retryDelay(request.identity, origin);
      if (retryDelay !== undefined) {
        throw originRateLimited(retryDelay);
      }
    }
    if (
      request.action.kind !== "navigate" &&
      request.action.kind !== "back" &&
      request.action.kind !== "forward"
    ) {
      return;
    }
    this.admitNavigation(
      request,
      origin,
      request.action.kind === "navigate",
    );
  }

  private admitNavigation(
    request: EngineRequest,
    origin: string,
    enforceAccessDenial: boolean,
  ): void {
    if (origin === "") {
      return;
    }
    if (enforceAccessDenial) {
      const denials = this.consecutiveAccessDenials.get(
        accessDenialKey(request, origin),
      );
      if (denials !== undefined && denials >= 3) {
        throw accessDenied(3, true);
      }
    }
    const retryDelay = this.originBudget?.retryDelay(
      request.identity,
      origin,
    );
    if (retryDelay !== undefined) {
      throw originRateLimited(retryDelay);
    }
    const navigationDelay = this.originBudget?.chargeNavigation(
      request.identity,
      origin,
    );
    if (navigationDelay !== undefined) {
      throw originRateLimited(navigationDelay);
    }
  }

  private async classifyActionOutcome(request: EngineRequest): Promise<void> {
    if (
      this.suspectedDocumentGeneration !== undefined &&
      this.documentTracker?.healthy === false
    ) {
      this.suspectedSticky = true;
    }
    const response = this.mainDocumentResponses.get(this.page);
    const currentDocumentGeneration = this.documentTracker?.generation;
    if (
      response === undefined &&
      (currentDocumentGeneration === undefined ||
        currentDocumentGeneration === this.classifiedDocumentGeneration)
    ) {
      return;
    }
    if (response !== undefined) {
      this.mainDocumentResponses.delete(this.page);
    }
    const challenge = await classifyChallenge(this.page);
    this.classifiedDocumentGeneration = currentDocumentGeneration;
    if (challenge === "required") {
      throw new EngineActionError(
        "BROWSER_CHALLENGE_REQUIRED",
        "Interactive website challenge requires human control",
        false,
        undefined,
        {
          site_outcome: "BROWSER_CHALLENGE_REQUIRED",
          classifier_rules_version: CLASSIFIER_RULES_VERSION,
        },
      );
    }
    if (challenge === "suspected") {
      this.suspectedDocumentGeneration =
        this.documentTracker?.generation ?? 1;
      this.suspectedSticky ||= this.documentTracker?.healthy === false;
      throw this.suspectedChallengeError();
    }
    if (this.suspectedDocumentGeneration !== undefined) {
      if (this.documentTracker?.healthy === false) {
        this.suspectedSticky = true;
      } else if (
        !this.suspectedSticky &&
        this.documentTracker !== undefined &&
        this.documentTracker.generation > this.suspectedDocumentGeneration
      ) {
        this.suspectedDocumentGeneration = undefined;
      }
    }
    if (response === undefined || response.origin === "") {
      return;
    }
    const denialKey = accessDenialKey(request, response.origin);
    if (response.status === 403) {
      const denials = Math.min(
        3,
        (this.consecutiveAccessDenials.get(denialKey) ?? 0) + 1,
      );
      this.consecutiveAccessDenials.set(denialKey, denials);
      throw accessDenied(denials, denials === 3);
    }
    this.consecutiveAccessDenials.delete(denialKey);
    if (response.status !== 429) {
      return;
    }
    const retryAfter = parseRetryAfter(response.retryAfter, Date.now());
    if (retryAfter !== undefined) {
      this.originBudget?.setRetryAfter(
        request.identity,
        response.origin,
        retryAfter,
      );
    }
    throw new EngineActionError(
      "BROWSER_RATE_LIMITED",
      "Website rate limit was reached",
      true,
      undefined,
      {
        site_outcome: "BROWSER_RATE_LIMITED",
        ...(retryAfter === undefined ? {} : { retry_after_ms: retryAfter }),
      },
    );
  }

  private suspectedChallengeError(): EngineActionError {
    return new EngineActionError(
      "BROWSER_CHALLENGE_SUSPECTED",
      "Interactive website challenge may be present",
      true,
      undefined,
      {
        site_outcome: "BROWSER_CHALLENGE_SUSPECTED",
        classifier_rules_version: CLASSIFIER_RULES_VERSION,
        ...(this.suspectedSticky
          ? { challenge_release_unavailable: true }
          : {}),
      },
    );
  }

  private refreshSuspectedSticky(): void {
    if (
      this.suspectedDocumentGeneration !== undefined &&
      this.documentTracker?.healthy === false
    ) {
      this.suspectedSticky = true;
    }
  }

  private denySuspectedMutation(): void {
    if (this.suspectedDocumentGeneration !== undefined) {
      if (this.documentTracker?.healthy === false) {
        this.suspectedSticky = true;
      }
      throw this.suspectedChallengeError();
    }
  }

  private async establishDocumentTracker(page: Page): Promise<void> {
    await this.documentTracker?.detach();
    this.documentTracker = undefined;
    this.classifiedDocumentGeneration = undefined;
    try {
      if (typeof this.documentTrackerFactory !== "function") {
        throw new Error("document tracker factory is missing");
      }
      this.documentTracker = await this.documentTrackerFactory(page);
    } catch {
      throw new EngineActionError(
        "BROWSER_RUNTIME_UNAVAILABLE",
        "Browser document identity tracking is unavailable",
        false,
      );
    }
  }

  async close(): Promise<void> {
    await this.context.close();
  }

  private configurePage(page: Page): void {
    if (this.configuredPages.has(page)) {
      return;
    }
    this.configuredPages.add(page);
    page.on("dialog", (dialog) => {
      void dialog.dismiss();
    });
    page.on("download", (download) => {
      void download.cancel();
    });
    page.on("response", (response) => {
      if (
        response.request().isNavigationRequest() &&
        response.frame() === page.mainFrame()
      ) {
        const headers = response.headers();
        if (gatewayBlockedResponse(headers)) {
          this.blockedNavigationPages.add(page);
        }
        this.mainDocumentResponses.set(page, {
          status: response.status(),
          origin: safeOrigin(response.url()),
          ...(headers["retry-after"] === undefined
            ? {}
            : { retryAfter: headers["retry-after"] }),
        });
      }
    });
    page.on("framenavigated", (frame) => {
      if (page === this.page && frame === page.mainFrame()) {
        this.navigationGeneration++;
      }
    });
  }

  private assertGatewayAllowed(page: Page): void {
    if (!this.blockedNavigationPages.has(page)) {
      return;
    }
    this.blockedNavigationPages.delete(page);
    throw new EngineActionError(
      "BROWSER_TARGET_BLOCKED",
      "Browser egress gateway blocked the navigation target",
      false,
    );
  }

  private async bindSession(
    request: EngineRequest,
    timeout: number,
  ): Promise<void> {
    const sessionKey = pageContinuationSessionKey(request.identity);
    const attachmentKey = [
      request.identity.attachment_id,
      request.identity.control_epoch,
    ].join("\u0000");
    if (
      this.boundSessionKey === sessionKey &&
      this.boundAttachmentKey === attachmentKey
    ) {
      return;
    }
    if (this.boundSessionKey === sessionKey) {
      this.suspectedDocumentGeneration = undefined;
      this.suspectedSticky = false;
      await this.establishDocumentTracker(this.page);
      this.boundAttachmentKey = attachmentKey;
      return;
    }
    this.boundSessionKey = "";
    this.boundAttachmentKey = "";
    const pages = this.context.pages().filter((page) => !page.isClosed());
    for (const extra of pages.slice(1)) {
      await extra.close();
    }
    const page = await this.context.newPage();
    this.configurePage(page);
    this.page = page;
    if (pages[0] !== undefined) {
      await pages[0].close();
    }
    this.lastBodyText = "";
    this.blockedNavigationPages.delete(page);
    page.setDefaultTimeout(timeout);
    page.setDefaultNavigationTimeout(timeout);
    await page.goto("about:blank", {
      timeout,
      waitUntil: "domcontentloaded",
    });
    await this.establishDocumentTracker(page);
    this.suspectedDocumentGeneration = undefined;
    this.suspectedSticky = false;
    this.boundAttachmentKey = attachmentKey;
    if (request.action.kind === "navigate") {
      this.boundSessionKey = sessionKey;
      return;
    }
    if (request.action.kind === "preflight") {
      // Preflight proves that the isolated engine can start without making a
      // saved public page part of readiness. It must not bind the Session:
      // the first real action still needs to restore that continuation.
      return;
    }
    const savedURL = await this.continuation.lookup(request.identity);
    if (savedURL !== undefined) {
      try {
        await page.goto(savedURL, {
          timeout,
          waitUntil: "domcontentloaded",
        });
      } catch (error) {
        const tunnelFailure = await this.classifyTunnelFailure(error, timeout);
        if (tunnelFailure !== undefined) {
          throw tunnelFailure;
        }
        throw new EngineActionError(
          "BROWSER_EGRESS_UNAVAILABLE",
          "saved Browser page could not be restored through the egress gateway",
          true,
        );
      }
    }
    this.boundSessionKey = sessionKey;
  }

  private async classifyTunnelFailure(
    error: unknown,
    _timeout: number,
  ): Promise<EngineActionError | undefined> {
    if (!isTunnelConnectionFailure(error)) {
      return undefined;
    }
    // Chromium does not expose the CONNECT response headers to the page. A
    // healthy Gateway therefore cannot distinguish a policy denial from a
    // public DNS/dial/upstream failure. Unknown tunnel failures must stay
    // recoverable instead of being guessed into a permanent security block.
    return new EngineActionError(
      "BROWSER_EGRESS_UNAVAILABLE",
      "Browser HTTPS tunnel is unavailable",
      true,
    );
  }

  private async executeAction(
    action: BrowserAction,
    timeout: number,
    request: EngineRequest,
  ): Promise<ActionEffect | undefined> {
    const page = this.page;
    page.setDefaultTimeout(timeout);
    page.setDefaultNavigationTimeout(timeout);
    switch (action.kind) {
      case "navigate":
        this.blockedNavigationPages.delete(page);
        await page.goto(action.url ?? "", {
          timeout,
          waitUntil: "domcontentloaded",
        });
        return;
      case "click": {
        const x = action.x ?? -1;
        const y = action.y ?? -1;
        assertViewportPoint(x, y);
        const target = await interactiveHandleAt(page, x, y);
        try {
          if (target.handle !== undefined && !blocksNonSecretTyping(target.metadata)) {
            this.denySuspectedMutation();
            await target.handle.focus();
            return {
              clickEffect: "focused",
              targetCategory: "text_input",
            };
          }
          if (
            target.handle === undefined ||
            blocksHighImpactActivation(target.metadata)
          ) {
            const blocked = await this.observe("semantic", timeout);
            throw new EngineActionError(
              "BROWSER_HIGH_IMPACT_ACTION_BLOCKED",
              "Phase 1 blocks this high-impact browser action",
              false,
              undefined,
              {
                target_category: targetCategory(target.metadata),
                page_state_id: blocked.page_state_id,
                navigation_generation: blocked.navigation_generation,
              },
            );
          }
          this.denySuspectedMutation();
          this.admitNavigation(
            request,
            safeOrigin(target.metadata.href),
            true,
          );
          await this.actAndAwaitNavigation(
            () => target.handle!.click({ timeout }),
            timeout,
          );
          return {
            clickEffect: "activated",
            targetCategory: "link",
          };
        } finally {
          await target.handle?.dispose();
        }
      }
      case "type_non_secret": {
        const target = await activeInteractiveHandle(page);
        try {
          if (
            target.handle === undefined ||
            blocksNonSecretTyping(target.metadata)
          ) {
            throw new EngineActionError(
              "BROWSER_USER_ACTION_REQUIRED",
              "credential fields require a later human-control phase",
              false,
            );
          }
          this.denySuspectedMutation();
          const current = await target.handle.evaluate((element) => {
            if (
              !(element instanceof HTMLInputElement) &&
              !(element instanceof HTMLTextAreaElement)
            ) {
              return undefined;
            }
            return {
              value: element.value,
              selectionStart: element.selectionStart,
              selectionEnd: element.selectionEnd,
            };
          });
          if (current === undefined) {
            throw new EngineActionError(
              "BROWSER_ACTION_REJECTED",
              "active text control is no longer actionable",
              false,
            );
          }
          const start = current.selectionStart ?? current.value.length;
          const end = current.selectionEnd ?? start;
          const result =
            current.value.slice(0, start) +
            (action.text ?? "") +
            current.value.slice(end);
          if (
            Buffer.byteLength(current.value, "utf8") > MAX_TYPE_VALUE_BYTES ||
            Buffer.byteLength(result, "utf8") > MAX_TYPE_VALUE_BYTES
          ) {
            throw new EngineActionError(
              "BROWSER_ACTION_REJECTED",
              "text control value exceeds the Phase 1 limit",
              false,
            );
          }
          await target.handle.fill(result, { timeout });
          return;
        } finally {
          await target.handle?.dispose();
        }
      }
      case "scroll":
        await page.mouse.wheel(action.delta_x ?? 0, action.delta_y ?? 0);
        return;
      case "keypress":
        {
          const metadata = await activeInteractiveMetadata(page);
          if (blocksKeypress(metadata, action.key ?? "")) {
            throw new EngineActionError(
              "BROWSER_HIGH_IMPACT_ACTION_BLOCKED",
              "Phase 1 blocks this browser key action",
              false,
            );
          }
          this.denySuspectedMutation();
          if (action.key === "Enter") {
            this.admitNavigation(
              request,
              safeOrigin(metadata.formAction),
              true,
            );
          }
        }
        await this.actAndAwaitNavigation(
          () => page.keyboard.press(action.key ?? ""),
          timeout,
        );
        return;
      case "select": {
        throw new EngineActionError(
          "BROWSER_USER_ACTION_REQUIRED",
          "Phase 1 does not allow select controls",
          false,
        );
      }
      case "wait":
        await page.waitForTimeout(action.duration_ms ?? 1);
        return;
      case "back":
        await this.traverseHistory(page, "back", timeout);
        return;
      case "forward":
        await this.traverseHistory(page, "forward", timeout);
        return;
      case "screenshot":
      case "checkpoint":
        return;
      case "preflight":
        if (!(await this.gatewayHealthProbe(Math.min(timeout, 1_000)))) {
          throw new EngineActionError(
            "BROWSER_EGRESS_UNAVAILABLE",
            "Browser egress gateway preflight failed",
            true,
          );
        }
        await page.goto("about:blank", {
          timeout,
          waitUntil: "domcontentloaded",
        });
        return;
      case "batch":
        for (const [index, nested] of (action.actions ?? []).entries()) {
          try {
            await this.executeAction(nested, timeout, request);
          } catch (error) {
            if (error instanceof EngineActionError) {
              throw new EngineActionError(
                error.code,
                error.message,
                error.recoverable,
                index,
                error.details,
              );
            }
            throw new EngineActionError(
              "BROWSER_RUNTIME_UNAVAILABLE",
              safeRuntimeMessage(
                error instanceof Error ? error.message : "browser batch action failed",
              ),
              true,
              index,
            );
          }
        }
        return;
    }
  }

  private async traverseHistory(
    page: Page,
    direction: "back" | "forward",
    timeout: number,
  ): Promise<void> {
    const startedAt = Date.now();
    if (direction === "back") {
      await page.goBack({ timeout, waitUntil: "commit" });
    } else {
      await page.goForward({ timeout, waitUntil: "commit" });
    }
    const remaining = Math.max(1, timeout - (Date.now() - startedAt));
    await page.waitForFunction(
      () => document.readyState !== "loading",
      undefined,
      { timeout: remaining },
    );
  }

  private async actAndAwaitNavigation(
    action: () => Promise<void>,
    timeout: number,
  ): Promise<void> {
    const page = this.page;
    const mainFrame = page.mainFrame();
    const navigationSettled = page
      .waitForEvent("request", {
        predicate: (request) =>
          request.isNavigationRequest() && request.frame() === mainFrame,
        timeout: Math.min(timeout, 750),
      })
      .catch(() => undefined)
      .then(async (request) => {
        if (request === undefined) {
          return;
        }
        await page.waitForEvent("framenavigated", {
          predicate: (frame) => frame === mainFrame,
          timeout,
        });
        await page.waitForLoadState("domcontentloaded", { timeout });
      });
    await action();
    await navigationSettled;
  }

  private async observe(
    mode: ObservationMode,
    timeout: number,
  ): Promise<Observation> {
    const page = this.page;
    let screenshot: Buffer | undefined;
    if (mode === "screenshot" || mode === "both") {
      screenshot = await page.screenshot({
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
    }
    const includeSemantic = mode === "semantic" || mode === "both";
    const body = page.locator("body");
    const [title, ariaSnapshot, bodyText] = await Promise.all([
      page.title(),
      includeSemantic
        ? extractBounded(
            () => body.ariaSnapshot({ timeout: Math.min(timeout, 2000) }),
            MAX_STATE_TEXT_BYTES,
          )
        : Promise.resolve<ExtractionResult>({ value: "", timedOut: false }),
      includeSemantic
        ? extractBounded(
            () => body.innerText({ timeout: Math.min(timeout, 2000) }),
            MAX_STATE_TEXT_BYTES,
          )
        : Promise.resolve<ExtractionResult>({ value: "", timedOut: false }),
    ]);
    let domDiff: ReturnType<typeof textDiff> | undefined;
    if (includeSemantic && !bodyText.failed) {
      domDiff = textDiff(this.lastBodyText, bodyText.value);
      this.lastBodyText = bodyText.value;
    }
    const boundedTitle = truncateUTF8(title, 2048);
    const pageStateID = createHash("sha256")
      .update("openlinker.browser.page-state.v2\u0000")
      .update(truncateUTF8(page.url(), 4096))
      .update("\u0000")
      .update(boundedTitle)
      .update("\u0000")
      .update(ariaSnapshot.value)
      .update("\u0000")
      .update(bodyText.value)
      .digest("hex");
    const observation: Observation = {
      page_state_id: pageStateID,
      viewport: {
        width: VIEWPORT.width,
        height: VIEWPORT.height,
      },
      navigation_generation: this.navigationGeneration,
      origin: safeOrigin(page.url()),
      title: boundedTitle,
    };
    if (screenshot !== undefined) {
      observation.screenshot = {
        mime_type: "image/jpeg",
        data: screenshot.toString("base64"),
        width: VIEWPORT.width,
        height: VIEWPORT.height,
      };
    }
    if (includeSemantic && !ariaSnapshot.failed) {
      observation.ax_tree = { aria_snapshot: ariaSnapshot.value };
    }
    if (domDiff !== undefined) {
      observation.dom_diff = domDiff;
    }
    if (ariaSnapshot.timedOut) {
      observation.ax_tree_timed_out = true;
    }
    if (bodyText.timedOut) {
      observation.dom_diff_timed_out = true;
    }
    return observation;
  }
}

export async function routePageWebSocket(
  controlState: ControlState,
  webSocket: WebSocketRoute,
): Promise<void> {
  if (
    controlState.controller === "human" &&
    controlState.pageWebSockets.size < MAX_HUMAN_PAGE_WEBSOCKETS
  ) {
    controlState.pageWebSockets.add(webSocket);
    webSocket.connectToServer();
    return;
  }
  await webSocket.close({
    code: 1008,
    reason: "Agent control blocks page WebSocket traffic",
  });
}

export async function restoreAgentPagePolicy(
  controlState: ControlState,
): Promise<void> {
  const sockets = [...controlState.pageWebSockets];
  await Promise.all(
    sockets.map((webSocket) =>
      webSocket.close({
        code: 1008,
        reason: "Human controller epoch ended",
      }),
    ),
  );
  controlState.pageWebSockets.clear();
  controlState.controller = "agent";
  controlState.attachmentKey = "";
}

interface ExtractionResult {
  value: string;
  timedOut: boolean;
  failed?: boolean;
}

interface ActionEffect {
  clickEffect: ClickEffect;
  targetCategory: "link" | "text_input";
}

interface MainDocumentResponse {
  status: number;
  origin: string;
  retryAfter?: string;
}

async function extractBounded(
  extract: () => Promise<string>,
  maximumBytes: number,
): Promise<ExtractionResult> {
  try {
    return {
      value: truncateUTF8(await extract(), maximumBytes),
      timedOut: false,
    };
  } catch (error) {
    return {
      value: "",
      timedOut: error instanceof playwrightErrors.TimeoutError,
      failed: true,
    };
  }
}

class EngineActionError extends Error {
  readonly code: BrowserErrorCode;
  readonly recoverable: boolean;
  readonly actionIndex: number | undefined;
  readonly details: EngineErrorDetails;

  constructor(
    code: BrowserErrorCode,
    message: string,
    recoverable: boolean,
    actionIndex?: number,
    details: EngineErrorDetails = {},
  ) {
    super(message);
    this.code = code;
    this.recoverable = recoverable;
    this.actionIndex = actionIndex;
    this.details = details;
  }
}

type EngineErrorDetails = Partial<
  Pick<
    EngineFailure,
    | "target_category"
    | "page_state_id"
    | "navigation_generation"
    | "site_outcome"
    | "retry_after_ms"
    | "classifier_rules_version"
    | "consecutive_access_denials"
    | "origin_blocked_for_attachment"
    | "challenge_release_unavailable"
  >
>;

function originRateLimited(retryAfterMS: number): EngineActionError {
  return new EngineActionError(
    "BROWSER_ORIGIN_RATE_LIMITED",
    "Browser origin budget is temporarily exhausted",
    true,
    undefined,
    {
      site_outcome: "BROWSER_ORIGIN_RATE_LIMITED",
      retry_after_ms: Math.max(1, Math.min(15 * 60 * 1000, retryAfterMS)),
    },
  );
}

function accessDenied(
  denials: number,
  blockedForAttachment: boolean,
): EngineActionError {
  return new EngineActionError(
    "BROWSER_ACCESS_DENIED",
    "Website denied access to the requested document",
    true,
    undefined,
    {
      site_outcome: "BROWSER_ACCESS_DENIED",
      consecutive_access_denials: Math.max(1, Math.min(3, denials)),
      ...(blockedForAttachment
        ? { origin_blocked_for_attachment: true }
        : {}),
    },
  );
}

function accessDenialKey(request: EngineRequest, origin: string): string {
  return [
    request.identity.attachment_id,
    request.identity.control_epoch,
    origin,
  ].join("\u0000");
}

function viewerAttachmentKey(
  identity: EngineViewerRequest["identity"],
): string {
  return [
    identity.attachment_id,
    identity.control_epoch,
    identity.controller,
  ].join("\u0000");
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

function assertAllowedPageURL(raw: string): void {
  if (raw === "about:blank" || isPublicHTTPURL(raw)) {
    return;
  }
  throw new EngineActionError(
    "BROWSER_TARGET_BLOCKED",
    "browser navigation reached a forbidden destination",
    false,
  );
}

interface InteractiveTarget {
  handle: ElementHandle<HTMLElement> | undefined;
  metadata: InteractiveMetadata;
}

async function interactiveHandleAt(
  page: Page,
  x: number,
  y: number,
): Promise<InteractiveTarget> {
  const candidate = await page.evaluateHandle(
    ({ pointX, pointY }) => {
      const element = document.elementFromPoint(pointX, pointY);
      const interactive = element?.closest(
        "a,button,input,select,textarea,[role=button],[role=link]",
      );
      return interactive instanceof HTMLElement ? interactive : null;
    },
    { pointX: x, pointY: y },
  );
  const element = candidate.asElement() as ElementHandle<HTMLElement> | null;
  if (element === null) {
    await candidate.dispose();
    return { handle: undefined, metadata: emptyInteractiveMetadata() };
  }
  return {
    handle: element,
    metadata: await metadataForHandle(element),
  };
}

async function activeInteractiveHandle(page: Page): Promise<InteractiveTarget> {
  const candidate = await page.evaluateHandle(() => {
    const active = document.activeElement;
    return active instanceof HTMLElement ? active : null;
  });
  const element = candidate.asElement() as ElementHandle<HTMLElement> | null;
  if (element === null) {
    await candidate.dispose();
    return { handle: undefined, metadata: emptyInteractiveMetadata() };
  }
  return {
    handle: element,
    metadata: await metadataForHandle(element),
  };
}

async function metadataForHandle(
  handle: ElementHandle<HTMLElement>,
): Promise<InteractiveMetadata> {
  return handle.evaluate((interactive) => {
    const input = interactive instanceof HTMLInputElement ? interactive : undefined;
    const anchor = interactive instanceof HTMLAnchorElement ? interactive : undefined;
    const textarea =
      interactive instanceof HTMLTextAreaElement ? interactive : undefined;
    const form = input?.form ?? textarea?.form;
    return {
      tagName: interactive.tagName.toLowerCase(),
      type: input?.type ?? "",
      autocomplete: input?.autocomplete ?? "",
      label: (
        interactive.getAttribute("aria-label") ??
        input?.labels?.[0]?.innerText ??
        textarea?.labels?.[0]?.innerText ??
        input?.placeholder ??
        textarea?.placeholder ??
        interactive.innerText ??
        ""
      ).slice(0, 512),
      href: (anchor?.href ?? "").slice(0, 1024),
      role: (interactive.getAttribute("role") ?? "").slice(0, 64),
      name: (input?.name ?? textarea?.name ?? "").slice(0, 256),
      placeholder: (input?.placeholder ?? textarea?.placeholder ?? "").slice(
        0,
        512,
      ),
      formMethod: (form?.method ?? "").slice(0, 16),
      formAction: (form?.action ?? "").slice(0, 1024),
      formRole: (form?.getAttribute("role") ?? "").slice(0, 64),
      download: anchor?.hasAttribute("download") ?? false,
    };
  });
}

function emptyInteractiveMetadata(): InteractiveMetadata {
  return {
    tagName: "",
    type: "",
    autocomplete: "",
    label: "",
    href: "",
    role: "",
    name: "",
    placeholder: "",
    formMethod: "",
    formAction: "",
    formRole: "",
    download: false,
  };
}

function targetCategory(metadata: InteractiveMetadata): TargetCategory {
  const tagName = metadata.tagName.toLowerCase();
  const role = metadata.role.toLowerCase();
  if (tagName === "") {
    return "none";
  }
  if (tagName === "a" || role === "link") {
    return "link";
  }
  if (
    (tagName === "input" &&
      ["text", "search"].includes(metadata.type.toLowerCase())) ||
    tagName === "textarea"
  ) {
    return "text_input";
  }
  if (
    tagName === "button" ||
    role === "button" ||
    (tagName === "input" &&
      ["button", "submit", "reset", "image"].includes(
        metadata.type.toLowerCase(),
      ))
  ) {
    return "button";
  }
  if (role !== "" || ["input", "select"].includes(tagName)) {
    return "custom";
  }
  return "other";
}

async function activeInteractiveMetadata(page: Page): Promise<InteractiveMetadata> {
  const target = await activeInteractiveHandle(page);
  try {
    return target.metadata;
  } finally {
    await target.handle?.dispose();
  }
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
