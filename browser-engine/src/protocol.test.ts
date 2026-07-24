import assert from "node:assert/strict";
import test from "node:test";

import { ENGINE_CONTRACT_ID, failure, parseRequest, truncateUTF8 } from "./protocol.js";

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

test("bounds UTF-8 error messages without splitting code points", () => {
  const message = "浏览器".repeat(300);
  const bounded = truncateUTF8(message, 500);
  assert.ok(Buffer.byteLength(bounded, "utf8") <= 500);
  assert.doesNotThrow(() => Buffer.from(bounded, "utf8").toString("utf8"));
  assert.ok(Buffer.byteLength(failure("1", "BROWSER_INTERNAL", message, false).error.message) <= 500);
});
