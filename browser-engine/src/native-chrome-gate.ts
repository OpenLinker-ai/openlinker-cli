import { randomUUID } from "node:crypto";
import { access } from "node:fs/promises";
import { createConnection } from "node:net";
import path from "node:path";

import type { BrowserContext, Page } from "playwright-core";

import type { EngineRequest, EngineViewerRequest } from "./protocol.js";

const CONTROL_CONTRACT = "openlinker.native-chrome.control.v1";
const MAX_RESPONSE_BYTES = 1024 * 1024;
const REQUIRED_CAPABILITIES = [
  "act",
  "back",
  "batch",
  "checkpoint",
  "click",
  "close",
  "forward",
  "full",
  "keypress",
  "navigate",
  "observe",
  "policy_evidence",
  "restricted",
  "screenshot",
  "scroll",
  "select",
  "semantic",
  "type_non_secret",
  "wait",
] as const;

type GateConfig = {
  socketPath: string;
  extensionRoot: string;
  extensionID: string;
  extensionVersion: string;
  activationPath: string;
  nativeHostProtocol: string;
  assetManifestSHA256: string;
};

type GateResponse = {
  contract_id: string;
  request_id: string;
  ok: boolean;
  result?: Record<string, unknown>;
  error?: string;
};

export class NativeChromeGate {
  private activationPage?: Page;

  private constructor(private readonly config: GateConfig) {}

  static fromEnvironment(environment: NodeJS.ProcessEnv): NativeChromeGate | undefined {
    const enabled = environment.OPENLINKER_NATIVE_CHROME_ENABLED;
    if (enabled === undefined || enabled === "false") return undefined;
    if (enabled !== "true") {
      throw new Error("OPENLINKER_NATIVE_CHROME_ENABLED must be true or false");
    }
    const config: GateConfig = {
      socketPath: requireAbsolutePath(
        environment.OPENLINKER_NATIVE_CHROME_SOCKET,
        "OPENLINKER_NATIVE_CHROME_SOCKET",
      ),
      extensionRoot: requireAbsolutePath(
        environment.OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT,
        "OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT",
      ),
      extensionID: requireExtensionID(
        environment.OPENLINKER_NATIVE_CHROME_EXTENSION_ID,
      ),
      extensionVersion: requireVersion(
        environment.OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION,
        "OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION",
      ),
      activationPath: requireActivationPath(
        environment.OPENLINKER_NATIVE_CHROME_ACTIVATION_PATH,
      ),
      nativeHostProtocol: requireOpaque(
        environment.OPENLINKER_NATIVE_CHROME_PROTOCOL,
        "OPENLINKER_NATIVE_CHROME_PROTOCOL",
      ),
      assetManifestSHA256: requireSHA256(
        environment.OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256,
      ),
    };
    return new NativeChromeGate(config);
  }

  ignoredDefaultArguments(): string[] {
    return ["--disable-extensions"];
  }

  isInternalPage(page: Page): boolean {
    return (
      page === this.activationPage ||
      page.url().startsWith(`chrome-extension://${this.config.extensionID}/`)
    );
  }

  async activate(context: BrowserContext): Promise<void> {
    await access(path.join(this.config.extensionRoot, "manifest.json"));
    const deadline = Date.now() + 30_000;
    let lastError: unknown = new Error(
      "official Chrome extension activation timed out",
    );
    while (Date.now() < deadline) {
      const activation = await context.newPage();
      try {
        await activation.goto(
          `chrome-extension://${this.config.extensionID}${this.config.activationPath}`,
          { timeout: 5_000, waitUntil: "domcontentloaded" },
        );
        await waitForSocket(
          this.config.socketPath,
          Math.max(1, Math.min(2_000, deadline - Date.now())),
        );
        await this.preflight();
        this.activationPage = activation;
        return;
      } catch (error) {
        lastError = error;
        await activation.close().catch(() => undefined);
        await new Promise((resolve) => setTimeout(resolve, 250));
      }
    }
    throw lastError;
  }

  async preflight(): Promise<void> {
    const result = await this.request("preflight", {
      asset_manifest_sha256: this.config.assetManifestSHA256,
      extension_id: this.config.extensionID,
      extension_version: this.config.extensionVersion,
      native_host_protocol: this.config.nativeHostProtocol,
      capabilities: [...REQUIRED_CAPABILITIES],
    });
    if (
      result.extension_id !== this.config.extensionID ||
      result.extension_version !== this.config.extensionVersion ||
      result.native_host_protocol !== this.config.nativeHostProtocol ||
      result.asset_manifest_sha256 !== this.config.assetManifestSHA256 ||
      !sameStringArray(result.capabilities, REQUIRED_CAPABILITIES)
    ) {
      throw new Error("official Chrome Native Host preflight evidence is invalid");
    }
  }

  async authorize(request: EngineRequest): Promise<void> {
    const result = await this.request("authorize_action", {
      browser_session_id: request.identity.browser_session_id,
      session_epoch: request.identity.session_epoch,
      attachment_id: request.identity.attachment_id,
      control_epoch: request.identity.control_epoch,
      interaction_policy: request.identity.browser_interaction_policy,
      interaction_policy_generation:
        request.identity.browser_interaction_policy_generation,
      mutation_origins_sha256:
        request.identity.browser_mutation_origins_sha256,
      action_kind: request.action.kind,
    });
    this.requireAuthorization(result);
  }

  async authorizeViewer(request: EngineViewerRequest): Promise<void> {
    const result = await this.request("authorize_viewer", {
      browser_session_id: request.identity.browser_session_id,
      session_epoch: request.identity.session_epoch,
      attachment_id: request.identity.attachment_id,
      control_epoch: request.identity.control_epoch,
      interaction_policy: request.identity.browser_interaction_policy,
      interaction_policy_generation:
        request.identity.browser_interaction_policy_generation,
      mutation_origins_sha256:
        request.identity.browser_mutation_origins_sha256,
      viewer_operation: request.operation,
    });
    this.requireAuthorization(result);
  }

  private requireAuthorization(result: Record<string, unknown>): void {
    if (
      Object.keys(result).sort().join(",") !== "authorized,nonce" ||
      result.authorized !== true ||
      typeof result.nonce !== "string" ||
      !/^[0-9a-f-]{36}$/.test(result.nonce)
    ) {
      throw new Error("official Chrome Native Host authorization is invalid");
    }
  }

  private async request(
    method: "preflight" | "authorize_action" | "authorize_viewer",
    params: Record<string, unknown>,
  ): Promise<Record<string, unknown>> {
    const requestID = randomUUID();
    const response = await exchange(this.config.socketPath, {
      contract_id: CONTROL_CONTRACT,
      request_id: requestID,
      method,
      params,
    });
    const responseFields = Object.keys(response).sort().join(",");
    if (
      (response.ok === true
        ? responseFields !== "contract_id,ok,request_id,result"
        : responseFields !== "contract_id,error,ok,request_id") ||
      response.contract_id !== CONTROL_CONTRACT ||
      response.request_id !== requestID ||
      response.ok !== true ||
      response.result === undefined ||
      response.error !== undefined
    ) {
      throw new Error(response.error ?? "official Chrome Native Host request failed");
    }
    return response.result;
  }
}

function exchange(
  socketPath: string,
  request: Record<string, unknown>,
): Promise<GateResponse> {
  return new Promise((resolve, reject) => {
    const socket = createConnection(socketPath);
    let buffered = "";
    const timeout = setTimeout(() => {
      socket.destroy();
      reject(new Error("official Chrome Native Host timed out"));
    }, 20_000);
    socket.setEncoding("utf8");
    socket.on("connect", () => socket.write(`${JSON.stringify(request)}\n`));
    socket.on("data", (chunk: string) => {
      buffered += chunk;
      if (Buffer.byteLength(buffered) > MAX_RESPONSE_BYTES) {
        socket.destroy(new Error("official Chrome Native Host response is too large"));
        return;
      }
      const newline = buffered.indexOf("\n");
      if (newline < 0) return;
      clearTimeout(timeout);
      try {
        const value: unknown = JSON.parse(buffered.slice(0, newline));
        if (value === null || typeof value !== "object" || Array.isArray(value)) {
          throw new Error("official Chrome Native Host response is invalid");
        }
        resolve(value as GateResponse);
      } catch (error) {
        reject(error);
      } finally {
        socket.end();
      }
    });
    socket.on("error", (error) => {
      clearTimeout(timeout);
      reject(error);
    });
  });
}

async function waitForSocket(socketPath: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      await access(socketPath);
      return;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
  throw new Error("official Chrome Native Host socket did not become ready");
}

function requireAbsolutePath(value: string | undefined, label: string): string {
  if (value === undefined || !path.isAbsolute(value) || path.normalize(value) !== value) {
    throw new Error(`${label} must be an absolute normalized path`);
  }
  return value;
}

function requireExtensionID(value: string | undefined): string {
  if (value === undefined || !/^[a-p]{32}$/.test(value)) {
    throw new Error("OPENLINKER_NATIVE_CHROME_EXTENSION_ID is invalid");
  }
  return value;
}

function requireVersion(value: string | undefined, label: string): string {
  if (value === undefined || !/^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/.test(value)) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function requireActivationPath(value: string | undefined): string {
  if (
    value === undefined ||
    !value.startsWith("/") ||
    value.includes("..") ||
    !/^\/[A-Za-z0-9._~!$&'()*+,;=:@%/-]+(?:\?[A-Za-z0-9._~!$&'()*+,;=:@%/?-]*)?$/.test(value)
  ) {
    throw new Error("OPENLINKER_NATIVE_CHROME_ACTIVATION_PATH is invalid");
  }
  return value;
}

function requireOpaque(value: string | undefined, label: string): string {
  if (value === undefined || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(value)) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function requireSHA256(value: string | undefined): string {
  if (value === undefined || !/^[0-9a-f]{64}$/.test(value)) {
    throw new Error("OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256 is invalid");
  }
  return value;
}

function sameStringArray(
  value: unknown,
  expected: readonly string[],
): boolean {
  return (
    Array.isArray(value) &&
    value.length === expected.length &&
    value.every((item, index) => item === expected[index])
  );
}

export { CONTROL_CONTRACT, REQUIRED_CAPABILITIES };
