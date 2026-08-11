import assert from "node:assert/strict";
import test from "node:test";

import { NativeChromeGate } from "./native-chrome-gate.js";

const validEnvironment = {
  OPENLINKER_NATIVE_CHROME_ENABLED: "true",
  OPENLINKER_NATIVE_CHROME_SOCKET: "/browser-tmp/native-host.sock",
  OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT:
    "/opt/openlinker/native-chrome/extension",
  OPENLINKER_NATIVE_CHROME_EXTENSION_ID:
    "abcdefghijklmnopabcdefghijklmnop",
  OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION: "1.2.3.4",
  OPENLINKER_NATIVE_CHROME_ACTIVATION_PATH:
    "/codex-sidepanel/index.html?openlinker=1",
  OPENLINKER_NATIVE_CHROME_PROTOCOL: "openlinker.native-chrome.v1",
  OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256: "a".repeat(64),
};

test("native Chrome gate is opt-in and enables the image-installed extension", () => {
  assert.equal(NativeChromeGate.fromEnvironment({}), undefined);
  const gate = NativeChromeGate.fromEnvironment(validEnvironment);
  assert.deepEqual(gate?.ignoredDefaultArguments(), ["--disable-extensions"]);
  assert.equal(
    gate?.isInternalPage({
      url: () =>
        "chrome-extension://abcdefghijklmnopabcdefghijklmnop/codex-sidepanel/index.html",
    } as never),
    true,
  );
});

test("native Chrome gate rejects incomplete or untrusted image configuration", () => {
  assert.throws(
    () =>
      NativeChromeGate.fromEnvironment({
        ...validEnvironment,
        OPENLINKER_NATIVE_CHROME_EXTENSION_ID: "invalid",
      }),
    /EXTENSION_ID/,
  );
  assert.throws(
    () =>
      NativeChromeGate.fromEnvironment({
        ...validEnvironment,
        OPENLINKER_NATIVE_CHROME_SOCKET: "relative.sock",
      }),
    /absolute normalized path/,
  );
});
