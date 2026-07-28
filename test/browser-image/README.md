# Browser image acceptance

This release acceptance gate drives the production Browser Runtime and Egress
Gateway images through the real UDS contract and a real headless Chromium.
It is separate from unit tests and runs in the Provider image PR/tag workflow,
because it creates a short-lived public HTTPS fixture through a pinned
Cloudflare Quick Tunnel. The tunnel has no uptime guarantee and is not a
production dependency.

Run from the CLI repository:

```sh
./test/browser-image/run.sh
```

The script exits non-zero when Docker, the public fixture, any image build, or
any assertion is unavailable; it has no skip path. The Provider image workflow
runs the same entry point before multi-architecture image builds, including
tagged releases. A successful unit-test or image-build job is not a substitute
for this gate.

If the pinned Quick Tunnel service is unavailable, an operator may supply an
equivalent temporary HTTPS endpoint backed by the same fixture image:

```sh
OPENLINKER_BROWSER_ACCEPTANCE_FIXTURE_URL=https://fixture.example \
  ./test/browser-image/run.sh
```

The override is not a skip: the harness verifies the fixture marker, exercises
the same Browser/Egress path, and checks the fixture metrics and packet trace.
If that endpoint requires a one-time public GET to establish non-sensitive
routing state, set `OPENLINKER_BROWSER_ACCEPTANCE_PRIME_URL` as well. The
priming URL is validated by the same public-target policy and is navigated only
after Browser preflight.

For an automatically created Quick Tunnel, readiness requires both an assigned
HTTPS URL and cloudflared's registered-connection event. The host does not
preflight that URL because host TLS interception can differ from the isolated
Browser/Egress path; the subsequent real Chromium title, semantic marker,
status counters, and packet assertions are the authoritative fixture check.
An operator-supplied URL still receives the host marker check before use.

For local diagnosis only, set
`OPENLINKER_BROWSER_ACCEPTANCE_KEEP_ON_FAILURE=1` to preserve the
prefix-named test containers, networks, and volumes after a failed run. The
default and CI behavior always clean them up.

The gate builds the current Browser, Egress, fixture, client, and packet
observer images, then proves:

- the Browser container has one internal network and no published ports;
- Browser preflight starts Chromium and checks the real Gateway;
- preflight reports the repository-locked Browser version, locale, timezone,
  distribution, and font-manifest evidence;
- the real Runtime UDS and Browser MCP server emit environment evidence only on
  the first tool result in one MCP session, then emit it again from a replacement
  MCP server instance representing Provider/MCP recovery; the separate
  Provider lifecycle test remains responsible for the real process boundary;
- a public HTTPS page can be observed and screenshotted;
- a non-autofocused search box can be focused without pointer activation,
  typed into, and submitted only as a public GET search;
- POST, WebSocket, and Service Worker traffic never reaches the fixture;
- private literals, private DNS, redirect-to-private, and controlled DNS
  rebinding fail closed;
- checkpoint/restore works for the same Browser Session;
- an ambiguous challenge remains gated across `pushState`, is released only
  after a clean cross-document classification, and is reclassified on a real
  back/forward cache restore;
- three ordinary 403 documents remain recoverable, while a fourth attempt is
  rejected locally without a fourth fixture request;
- a 429 `Retry-After` is bounded and enforced locally without another request;
- a high-confidence challenge fences and terminates only its attachment, after
  which a fresh attachment can preflight successfully;
- a different Browser Session starts blank;
- closing an attachment survives a new UDS client connection;
- direct TCP and DNS/UDP from the Browser namespace do not escape;
- stopping the Gateway produces a recoverable egress error without direct
  fallback; and
- Chromium has the required QUIC, DoH, and WebRTC restrictions and exposes no
  remote-debugging TCP port.

The no-bypass half is behavioral, not a launch-flag proxy. A test-only
`tcpdump` observer shares the Browser Runtime network namespace and captures
all interfaces while real Chromium:

- resolves and opens a randomized public HTTPS hostname;
- attempts a same-origin WebTransport connection;
- performs a WebRTC offer with a public STUN endpoint; and
- retries public navigation after the Gateway is stopped.

The captured trace must contain real TCP traffic to the Egress Gateway, zero
UDP packets, and zero TCP packets to any non-Gateway destination. Chromium
launch flags remain a secondary configuration assertion. Separate direct
TCP/DNS probes then prove the container network itself also has no fallback.
The DNS-rebinding probe uses a unique per-run hostname with a one-second TTL.
The harness first proves the public phase from the same Docker public network,
then real Chromium sends the same hostname through the production Gateway and
must receive its permanent private-target block. This prevents recursive DNS
caches from turning the two-phase assertion into a stale external-service
result.

The harness never receives an OpenLinker token, Provider key, Browser Profile
key, or channel credential. Its fixture and client images are test-only and
are not referenced by production Compose or image targets.
