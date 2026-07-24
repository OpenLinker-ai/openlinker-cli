import assert from "node:assert/strict";
import test from "node:test";

import {
  blocksHighImpactActivation,
  blocksNonSecretTyping,
  type InteractiveMetadata,
} from "./policy.js";

const SAFE: InteractiveMetadata = {
  tagName: "button",
  type: "button",
  autocomplete: "",
  label: "Search",
  href: "",
};

test("blocks credential fields while preserving ordinary inputs", () => {
  assert.equal(blocksNonSecretTyping(SAFE), false);
  assert.equal(blocksNonSecretTyping({ ...SAFE, type: "password" }), true);
  assert.equal(blocksNonSecretTyping({ ...SAFE, autocomplete: "one-time-code" }), true);
  assert.equal(blocksNonSecretTyping({ ...SAFE, autocomplete: "cc-number" }), true);
});

test("blocks explicit high-impact activation labels", () => {
  assert.equal(blocksHighImpactActivation(SAFE), false);
  assert.equal(blocksHighImpactActivation({ ...SAFE, type: "file" }), true);
  assert.equal(blocksHighImpactActivation({ ...SAFE, label: "Place order" }), true);
  assert.equal(blocksHighImpactActivation({ ...SAFE, label: "Delete account" }), true);
  assert.equal(
    blocksHighImpactActivation({ ...SAFE, href: "https://example.com/transfer-money" }),
    true,
  );
});
