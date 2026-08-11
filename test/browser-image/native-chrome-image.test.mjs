import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmod, mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import {
  buildAssetsLock,
  CAPABILITIES,
  extensionIDFromKey,
} from "./build-native-chrome-assets-lock.mjs";
import { verifyExtensionLock } from "./verify-native-chrome-extension-lock.mjs";

test("extension build input is pinned by exact ID, version and digest", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "native-extension-lock-"));
  const artifact = path.join(root, "extension.tar");
  const bytes = Buffer.from("locked extension");
  await writeFile(artifact, bytes);
  const lock = {
    activation_path: "/codex-sidepanel/index.html",
    extension_id: "abcdefghijklmnopabcdefghijklmnop",
    filename: "extension.tar",
    sha256: createHash("sha256").update(bytes).digest("hex"),
    source_reference: "authorized-build-input",
    version: "1.2.3.4",
  };
  const lockPath = path.join(root, "extension.lock.json");
  await writeFile(lockPath, JSON.stringify(lock));
  assert.deepEqual(
    await verifyExtensionLock(
      lockPath,
      artifact,
      lock.extension_id,
      lock.version,
    ),
    lock,
  );
  await writeFile(artifact, "tampered");
  await assert.rejects(
    verifyExtensionLock(lockPath, artifact, lock.extension_id, lock.version),
    /digest/,
  );
});

test("native Chrome Dockerfile has no runtime download or exposed control port", async () => {
  const dockerfile = await readFile(
    new URL("../../Dockerfile.browser.native-chrome", import.meta.url),
    "utf8",
  );
  for (const required of [
    "OPENLINKER_CHROME_ARTIFACT",
    "OPENLINKER_EXTENSION_ARTIFACT",
    "verify-native-chrome-extension-lock.mjs",
    "build-native-chrome-assets-lock.mjs",
    "/opt/google/chrome/extensions/hehggadaopoacecdllhhajmbjkdcmajg.json",
    "/etc/opt/chrome/policies/managed/openlinker-native-chrome.json",
    "find /opt/openlinker/native-chrome/extension -type f -exec chmod 0444 {} +",
    "find /opt/openlinker/native-chrome/src -type d -exec chmod 0555 {} +",
    "find /opt/openlinker/native-chrome/src -type f -exec chmod 0444 {} +",
    "chmod 4755 /opt/google/chrome/chrome-sandbox",
    "USER 10001:10001",
  ]) {
    assert.match(dockerfile, new RegExp(required.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  for (const forbidden of [
    "clients2.google.com",
    "--load-extension",
    "--disable-extensions-except",
    "--no-sandbox",
    "--remote-debugging-port",
    "seccomp=unconfined",
    "EXPOSE ",
  ]) {
    assert.equal(dockerfile.includes(forbidden), false, forbidden);
  }
});

test("final runtime lock covers every installed native component", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "native-assets-lock-"));
  const chromeRoot = path.join(root, "chrome");
  const extensionRoot = path.join(root, "extension");
  await mkdir(chromeRoot);
  await mkdir(path.join(extensionRoot, "codex-sidepanel"), { recursive: true });
  const extensionInstallRoot = path.join(root, "chrome-extensions");
  const extensionPolicyRoot = path.join(root, "chrome-policies");
  await mkdir(extensionInstallRoot);
  await mkdir(extensionPolicyRoot);
  const extensionKey = Buffer.from("locked-test-extension-public-key").toString("base64");
  const extensionID = extensionIDFromKey(extensionKey);
  const paths = {
    chrome: path.join(chromeRoot, "chrome"),
    chromeSandbox: path.join(chromeRoot, "chrome-sandbox"),
    manifest: path.join(extensionRoot, "manifest.json"),
    crx: path.join(extensionRoot, "extension.crx"),
    extensionFile: path.join(extensionRoot, "worker.js"),
    activationFile: path.join(extensionRoot, "codex-sidepanel", "index.html"),
    host: path.join(root, "native-host"),
    hostSource: path.join(root, "native-host.mjs"),
    framing: path.join(root, "native-framing.mjs"),
    engine: path.join(root, "native-engine"),
    extensionInstallManifest: path.join(extensionInstallRoot, `${extensionID}.json`),
    extensionPolicy: path.join(extensionPolicyRoot, "openlinker-native-chrome.json"),
    nativeManifest: path.join(root, "native-manifest.json"),
    output: path.join(root, "assets.lock.json"),
  };
  for (const file of [
    paths.chrome,
    paths.chromeSandbox,
    paths.extensionFile,
    paths.activationFile,
    paths.host,
    paths.hostSource,
    paths.framing,
    paths.engine,
  ]) {
    await writeFile(file, path.basename(file), { mode: 0o555 });
    await chmod(file, 0o555);
  }
  await writeFile(
    paths.manifest,
    JSON.stringify({
      manifest_version: 3,
      version: "1.2.3.4",
      key: extensionKey,
    }),
    { mode: 0o444 },
  );
  await chmod(paths.manifest, 0o444);
  await writeFile(paths.crx, Buffer.concat([Buffer.from("Cr24"), Buffer.alloc(12)]), {
    mode: 0o444,
  });
  await chmod(paths.crx, 0o444);
  const chromeLockPath = path.join(root, "chrome.lock.json");
  const extensionLockPath = path.join(root, "extension.lock.json");
  await writeFile(chromeLockPath, JSON.stringify({ version: "150.0.1.2" }));
  await writeFile(
    extensionLockPath,
    JSON.stringify({
      extension_id: extensionID,
      version: "1.2.3.4",
      activation_path: "/codex-sidepanel/index.html",
    }),
  );
  const lock = await buildAssetsLock({
    chromeLockPath,
    extensionLockPath,
    chromeRoot,
    chromePath: paths.chrome,
    extensionRoot,
    extensionInstallManifestPath: paths.extensionInstallManifest,
    extensionPolicyPath: paths.extensionPolicy,
    nativeHostPath: paths.host,
    nativeHostSourcePath: paths.hostSource,
    nativeFramingSourcePath: paths.framing,
    enginePath: paths.engine,
    nativeMessagingManifestPath: paths.nativeManifest,
    architecture: "amd64",
    nativeHostProtocol: "openlinker.native-chrome.v1",
    profileGeneration: 1,
    outputPath: paths.output,
  });
  assert.deepEqual(lock.capabilities, CAPABILITIES);
  assert.equal(lock.assets.some((asset) => asset.path === paths.chrome), true);
  assert.equal(lock.assets.some((asset) => asset.path === paths.manifest), true);
  assert.equal(lock.assets.some((asset) => asset.path === paths.crx), true);
  assert.equal(
    lock.assets.some((asset) => asset.path === paths.extensionInstallManifest),
    true,
  );
  assert.equal(lock.assets.some((asset) => asset.path === paths.extensionPolicy), true);
  assert.equal(lock.assets.some((asset) => asset.path === paths.nativeManifest), true);
  await chmod(paths.manifest, 0o644);
  await writeFile(
    paths.manifest,
    JSON.stringify({
      manifest_version: 3,
      version: "9.9.9.9",
      key: extensionKey,
    }),
  );
  await chmod(paths.manifest, 0o444);
  await assert.rejects(
    buildAssetsLock({
      chromeLockPath,
      extensionLockPath,
      chromeRoot,
      chromePath: paths.chrome,
      extensionRoot,
      extensionInstallManifestPath: paths.extensionInstallManifest,
      extensionPolicyPath: paths.extensionPolicy,
      nativeHostPath: paths.host,
      nativeHostSourcePath: paths.hostSource,
      nativeFramingSourcePath: paths.framing,
      enginePath: paths.engine,
      nativeMessagingManifestPath: paths.nativeManifest,
      architecture: "amd64",
      nativeHostProtocol: "openlinker.native-chrome.v1",
      profileGeneration: 1,
      outputPath: paths.output,
    }),
    /manifest identity/,
  );
});
