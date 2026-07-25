export interface InteractiveMetadata {
  tagName: string;
  type: string;
  autocomplete: string;
  label: string;
  href: string;
}

const CREDENTIAL_AUTOCOMPLETE = new Set([
  "current-password",
  "new-password",
  "one-time-code",
  "cc-number",
  "cc-csc",
  "cc-exp",
]);

const HIGH_IMPACT_PATTERN =
  /\b(buy|purchase|pay|place order|confirm order|transfer|send money|delete account|close account|publish|post publicly|sign contract|accept offer)\b/i;

export function blocksNonSecretTyping(metadata: InteractiveMetadata): boolean {
  return (
    metadata.type.toLowerCase() === "password" ||
    CREDENTIAL_AUTOCOMPLETE.has(metadata.autocomplete.toLowerCase())
  );
}

export function blocksHighImpactActivation(metadata: InteractiveMetadata): boolean {
  if (metadata.type.toLowerCase() === "file") {
    return true;
  }
  const label = `${metadata.label} ${metadata.href}`.trim();
  return HIGH_IMPACT_PATTERN.test(label);
}
