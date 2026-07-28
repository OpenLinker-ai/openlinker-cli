import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import path from "node:path";

const [lockPath, artifactPath, expectedDistribution, expectedVersion] =
  process.argv.slice(2);
if (
  lockPath === undefined ||
  artifactPath === undefined ||
  !["google_chrome", "chrome_for_testing"].includes(expectedDistribution) ||
  !/^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/.test(expectedVersion ?? "")
) {
  throw new Error("Chrome lock verifier arguments are invalid");
}
const raw = await readFile(lockPath, "utf8");
const value = JSON.parse(raw);
const keys = Object.keys(value).sort();
const expectedKeys = [
  "distribution",
  "filename",
  "sha256",
  "source_reference",
  "version",
].sort();
for (const key of expectedKeys) {
  if (raw.match(new RegExp(`"${key}"`, "g"))?.length !== 1) {
    throw new Error("Chrome lock record contains a duplicate or missing field");
  }
}
if (
  keys.length !== expectedKeys.length ||
  keys.some((key, index) => key !== expectedKeys[index]) ||
  value.distribution !== expectedDistribution ||
  value.version !== expectedVersion ||
  value.filename !== path.basename(artifactPath) ||
  typeof value.source_reference !== "string" ||
  value.source_reference.length < 1 ||
  value.source_reference.length > 512 ||
  !/^[0-9a-f]{64}$/.test(value.sha256)
) {
  throw new Error("Chrome lock record is invalid or does not match build arguments");
}
const digest = createHash("sha256")
  .update(await readFile(artifactPath))
  .digest("hex");
if (digest !== value.sha256) {
  throw new Error("Chrome artifact digest does not match its lock record");
}
