#!/bin/sh
set -eu
CDPATH=
export CDPATH

test_root=$(cd -- "$(dirname -- "$0")" && pwd)
temporary_root=$(mktemp -d "${TMPDIR:-/tmp}/openlinker-release-images.XXXXXX")
cleanup() {
  rm -rf -- "$temporary_root"
}
trap cleanup EXIT INT TERM

PATH="$test_root/fixtures:$PATH" \
  OPENLINKER_RELEASE_EVIDENCE_DIR="$temporary_root/evidence" \
  "$test_root/verify.sh" registry.example/openlinker v1.2.3 \
  >/dev/null

evidence_count=$(
  find "$temporary_root/evidence" -type f | wc -l | tr -d ' '
)
if [ "$evidence_count" != "12" ]; then
  echo "release verifier wrote $evidence_count evidence files, want 12" >&2
  exit 1
fi

if PATH="$test_root/fixtures:$PATH" \
  FAKE_MISSING_SBOM=1 \
  "$test_root/verify.sh" registry.example/openlinker v1.2.3 \
  >/dev/null 2>&1; then
  echo "release verifier accepted an image without an SPDX SBOM" >&2
  exit 1
fi

echo "Release image evidence verifier self-test passed"
