import { isIP } from "node:net";

export const ENGINE_CONTRACT_ID = "openlinker.browser.engine.v1";

export const ACTION_KINDS = [
  "navigate",
  "click",
  "type_non_secret",
  "scroll",
  "keypress",
  "select",
  "wait",
  "back",
  "forward",
  "screenshot",
] as const;

export type ActionKind = (typeof ACTION_KINDS)[number];

export interface Identity {
  run_id: string;
  agent_id: string;
  principal_scope_id: string;
  conversation_id: string;
  browser_generation: number;
  attachment_id: string;
  control_epoch: number;
}

export interface BrowserAction {
  kind: ActionKind;
  url?: string;
  x?: number;
  y?: number;
  delta_x?: number;
  delta_y?: number;
  text?: string;
  key?: string;
  value?: string;
  duration_ms?: number;
}

export interface EngineRequest {
  contract_id: typeof ENGINE_CONTRACT_ID;
  action_id: string;
  deadline: string;
  identity: Identity;
  action: BrowserAction;
}

export type BrowserErrorCode =
  | "BROWSER_OUTPUT_TOO_LARGE"
  | "BROWSER_RUNTIME_UNAVAILABLE"
  | "BROWSER_EGRESS_UNAVAILABLE"
  | "BROWSER_TARGET_BLOCKED"
  | "BROWSER_PROFILE_LOCKED"
  | "BROWSER_PROFILE_CORRUPT"
  | "BROWSER_CONVERSATION_RECOVERY_FAILED"
  | "BROWSER_USER_ACTION_REQUIRED"
  | "BROWSER_HIGH_IMPACT_ACTION_BLOCKED"
  | "BROWSER_ACTION_LIMIT_EXCEEDED"
  | "BROWSER_CANCELED"
  | "BROWSER_OUTPUT_INVALID"
  | "BROWSER_INTERNAL";

export interface EngineFailure {
  code: BrowserErrorCode;
  message: string;
  recoverable: boolean;
}

export interface Observation {
  page_state_id: string;
  screenshot?: {
    mime_type: "image/jpeg";
    data: string;
  };
  ax_tree?: unknown;
  dom_diff?: unknown;
  origin?: string;
  title?: string;
}

export type EngineResponse =
  | {
      contract_id: typeof ENGINE_CONTRACT_ID;
      action_id: string;
      status: "ok";
      observation: Observation;
    }
  | {
      contract_id: typeof ENGINE_CONTRACT_ID;
      action_id: string;
      status: "error";
      error: EngineFailure;
    };

const REQUEST_FIELDS = new Set([
  "contract_id",
  "action_id",
  "deadline",
  "identity",
  "action",
]);
const IDENTITY_FIELDS = new Set([
  "run_id",
  "agent_id",
  "principal_scope_id",
  "conversation_id",
  "browser_generation",
  "attachment_id",
  "control_epoch",
]);
const ACTION_FIELDS = new Set([
  "kind",
  "url",
  "x",
  "y",
  "delta_x",
  "delta_y",
  "text",
  "key",
  "value",
  "duration_ms",
]);

export function parseRequest(line: string, now = Date.now()): EngineRequest {
  if (Buffer.byteLength(line, "utf8") > 256 * 1024) {
    throw new Error("engine request exceeds input limit");
  }
  const value: unknown = JSON.parse(line);
  const request = requireRecord(value, "request");
  requireExactFields(request, REQUEST_FIELDS, "request");
  if (request.contract_id !== ENGINE_CONTRACT_ID) {
    throw new Error("unsupported engine contract");
  }
  const actionID = requireString(request.action_id, "action_id", 1, 32);
  if (!/^[1-9][0-9]*$/.test(actionID)) {
    throw new Error("action_id is invalid");
  }
  const deadline = requireString(request.deadline, "deadline", 20, 64);
  const deadlineMillis = Date.parse(deadline);
  if (
    !Number.isFinite(deadlineMillis) ||
    deadlineMillis <= now ||
    deadlineMillis > now + 60_000
  ) {
    throw new Error("deadline has elapsed");
  }
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    deadline,
    identity: parseIdentity(request.identity),
    action: parseAction(request.action),
  };
}

function parseIdentity(value: unknown): Identity {
  const identity = requireRecord(value, "identity");
  requireExactFields(identity, IDENTITY_FIELDS, "identity");
  const parsed: Identity = {
    run_id: requireUUID(identity.run_id, "run_id"),
    agent_id: requireUUID(identity.agent_id, "agent_id"),
    principal_scope_id: requireOpaque(identity.principal_scope_id, "principal_scope_id", 256),
    conversation_id: requireUUID(identity.conversation_id, "conversation_id"),
    browser_generation: requirePositiveInteger(identity.browser_generation, "browser_generation"),
    attachment_id: requireUUID(identity.attachment_id, "attachment_id"),
    control_epoch: requirePositiveInteger(identity.control_epoch, "control_epoch"),
  };
  return parsed;
}

function parseAction(value: unknown): BrowserAction {
  const action = requireRecord(value, "action");
  requireKnownFields(action, ACTION_FIELDS, "action");
  const kind = requireString(action.kind, "kind", 1, 64);
  if (!ACTION_KINDS.includes(kind as ActionKind)) {
    throw new Error("action kind is not allowed");
  }
  const parsed: BrowserAction = { kind: kind as ActionKind };
  for (const field of ["url", "text", "key", "value"] as const) {
    if (action[field] !== undefined) {
      const maximum =
        field === "text" ? 16 * 1024 : field === "url" ? 4096 : 2048;
      parsed[field] = requireString(action[field], field, 1, maximum);
    }
  }
  for (const field of ["x", "y", "delta_x", "delta_y", "duration_ms"] as const) {
    if (action[field] !== undefined) {
      parsed[field] = requireInteger(action[field], field);
    }
  }
  validateActionShape(parsed);
  return parsed;
}

function validateActionShape(action: BrowserAction): void {
  const present = new Set(
    Object.entries(action)
      .filter(([field, value]) => field !== "kind" && value !== undefined)
      .map(([field]) => field),
  );
  const requireFields = (...fields: string[]): void => {
    if (present.size !== fields.length || fields.some((field) => !present.has(field))) {
      throw new Error(`invalid ${action.kind} action shape`);
    }
  };
  switch (action.kind) {
    case "navigate":
      requireFields("url");
      if (!isPublicHTTPURL(action.url ?? "")) {
        throw new Error("navigate URL is invalid");
      }
      return;
    case "click":
      requireFields("x", "y");
      validateCoordinates(action);
      return;
    case "type_non_secret":
      requireFields("text");
      return;
    case "scroll":
      if (
        present.size === 0 ||
        [...present].some((field) => field !== "delta_x" && field !== "delta_y") ||
        ((action.delta_x ?? 0) === 0 && (action.delta_y ?? 0) === 0)
      ) {
        throw new Error("invalid scroll action shape");
      }
      for (const delta of [action.delta_x ?? 0, action.delta_y ?? 0]) {
        if (Math.abs(delta) > 32768) {
          throw new Error("scroll delta is out of range");
        }
      }
      return;
    case "keypress":
      requireFields("key");
      if (
        ![
          "Enter",
          "Tab",
          "Escape",
          "ArrowUp",
          "ArrowDown",
          "ArrowLeft",
          "ArrowRight",
          "PageUp",
          "PageDown",
          "Home",
          "End",
          "Backspace",
          "Delete",
          "Space",
        ].includes(action.key ?? "")
      ) {
        throw new Error("keypress key is not allowed");
      }
      return;
    case "select":
      requireFields("x", "y", "value");
      validateCoordinates(action);
      return;
    case "wait":
      requireFields("duration_ms");
      if ((action.duration_ms ?? 0) < 1 || (action.duration_ms ?? 0) > 5000) {
        throw new Error("wait duration is out of range");
      }
      return;
    case "back":
    case "forward":
    case "screenshot":
      requireFields();
      return;
  }
}

function validateCoordinates(action: BrowserAction): void {
  for (const coordinate of [action.x, action.y]) {
    if (coordinate === undefined || coordinate < 0 || coordinate > 32768) {
      throw new Error("coordinates are out of range");
    }
  }
}

function isPublicHTTPURL(raw: string): boolean {
  try {
    const url = new URL(raw);
    return (
      (url.protocol === "http:" || url.protocol === "https:") &&
      url.username === "" &&
      url.password === "" &&
      isIP(url.hostname) === 0 &&
      url.hostname.includes(".") &&
      !url.hostname.endsWith(".local") &&
      !url.hostname.endsWith(".internal")
    );
  } catch {
    return false;
  }
}

function requireRecord(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value as Record<string, unknown>;
}

function requireExactFields(
  value: Record<string, unknown>,
  fields: ReadonlySet<string>,
  label: string,
): void {
  requireKnownFields(value, fields, label);
  if ([...fields].some((field) => !(field in value))) {
    throw new Error(`${label} is missing a field`);
  }
}

function requireKnownFields(
  value: Record<string, unknown>,
  fields: ReadonlySet<string>,
  label: string,
): void {
  if (Object.keys(value).some((field) => !fields.has(field))) {
    throw new Error(`${label} contains an unknown field`);
  }
}

function requireString(
  value: unknown,
  label: string,
  minimum: number,
  maximum: number,
): string {
  if (
    typeof value !== "string" ||
    Buffer.byteLength(value, "utf8") < minimum ||
    Buffer.byteLength(value, "utf8") > maximum
  ) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function requireUUID(value: unknown, label: string): string {
  const parsed = requireString(value, label, 36, 36);
  if (
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(parsed) ||
    parsed === "00000000-0000-0000-0000-000000000000"
  ) {
    throw new Error(`${label} is invalid`);
  }
  return parsed;
}

function requireOpaque(value: unknown, label: string, maximum: number): string {
  const parsed = requireString(value, label, 1, maximum);
  if (!/^[A-Za-z0-9._:-]+$/.test(parsed)) {
    throw new Error(`${label} is invalid`);
  }
  return parsed;
}

function requirePositiveInteger(value: unknown, label: string): number {
  const parsed = requireInteger(value, label);
  if (parsed < 1) {
    throw new Error(`${label} must be positive`);
  }
  return parsed;
}

function requireInteger(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) {
    throw new Error(`${label} must be an integer`);
  }
  return value;
}

export function success(actionID: string, observation: Observation): EngineResponse {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    status: "ok",
    observation,
  };
}

export function failure(
  actionID: string,
  code: BrowserErrorCode,
  message: string,
  recoverable: boolean,
): Extract<EngineResponse, { status: "error" }> {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    status: "error",
    error: {
      code,
      message: truncateUTF8(message.trim(), 500),
      recoverable,
    },
  };
}

export function truncateUTF8(value: string, maximumBytes: number): string {
  if (Buffer.byteLength(value, "utf8") <= maximumBytes) {
    return value;
  }
  let low = 0;
  let high = value.length;
  while (low < high) {
    const middle = Math.ceil((low + high) / 2);
    if (Buffer.byteLength(value.slice(0, middle), "utf8") <= maximumBytes) {
      low = middle;
    } else {
      high = middle - 1;
    }
  }
  return value.slice(0, low);
}
