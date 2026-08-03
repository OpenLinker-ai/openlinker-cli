#!/bin/sh
set -eu
CDPATH=
export CDPATH

command -v docker >/dev/null 2>&1 || {
  echo "docker is required" >&2
  exit 1
}
command -v curl >/dev/null 2>&1 || {
  echo "curl is required" >&2
  exit 1
}

repository_root=$(cd -- "$(dirname -- "$0")/../.." && pwd)
suffix=$$
prefix="ol-browser-image-${suffix}"
provider_live_nonce=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
case "$provider_live_nonce" in
  ????????????????????????????????) ;;
  *)
    echo "cannot generate the Browser Provider fixture marker" >&2
    exit 1
    ;;
esac
provider_live_marker="provider-live-${provider_live_nonce}"
tunnel_network="${prefix}-tunnel"
internal_network="${prefix}-internal"
public_network="${prefix}-public"
fixture_container="${prefix}-fixture"
tunnel_container="${prefix}-tunnel"
collector_tunnel_container="${prefix}-collector-tunnel"
egress_container="${prefix}-egress"
runtime_container="${prefix}-runtime"
observer_container="${prefix}-observer"
control_volume="${prefix}-control"
key_volume="${prefix}-key"
state_volume="${prefix}-state"
capture_volume="${prefix}-capture"

browser_image="openlinker-browser-runtime:image-acceptance"
egress_image="openlinker-egress-gateway:image-acceptance"
client_image="openlinker-browser-acceptance-client:image-acceptance"
fixture_image="openlinker-browser-acceptance-fixture:image-acceptance"
observer_image="openlinker-browser-acceptance-observer:image-acceptance"
cloudflared_image="cloudflare/cloudflared@sha256:e39ee8da81ad5e05d77f38d2f51c60ca51bf2a8450ac3abab50c17fdb91d91bf"

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    for diagnostic_container in \
      "$runtime_container" \
      "$egress_container" \
      "$observer_container"; do
      docker logs "$diagnostic_container" >&2 || true
    done
    if [ "${OPENLINKER_BROWSER_ACCEPTANCE_KEEP_ON_FAILURE:-0}" = "1" ]; then
      echo "Browser image acceptance resources preserved with prefix ${prefix}" >&2
      return "$status"
    fi
  fi
  docker rm -f \
    "$runtime_container" \
    "$observer_container" \
    "$egress_container" \
    "$collector_tunnel_container" \
    "$tunnel_container" \
    "$fixture_container" \
    >/dev/null 2>&1 || true
  docker network rm \
    "$internal_network" \
    "$public_network" \
    "$tunnel_network" \
    >/dev/null 2>&1 || true
  docker volume rm \
    "$control_volume" \
    "$key_volume" \
    "$state_volume" \
    "$capture_volume" \
    >/dev/null 2>&1 || true
  return "$status"
}
trap cleanup EXIT INT TERM

docker build \
  -f "$repository_root/Dockerfile.browser" \
  -t "$browser_image" \
  "$repository_root"
docker build \
  --target egress \
  -f "$repository_root/Dockerfile.providers" \
  -t "$egress_image" \
  "$repository_root"
docker build \
  --target client \
  -f "$repository_root/test/browser-image/Dockerfile" \
  -t "$client_image" \
  "$repository_root"
docker build \
  --target fixture \
  -f "$repository_root/test/browser-image/Dockerfile" \
  -t "$fixture_image" \
  "$repository_root"
docker build \
  --target observer \
  -f "$repository_root/test/browser-image/Dockerfile" \
  -t "$observer_image" \
  "$repository_root"
docker pull "$cloudflared_image" >/dev/null

docker network create "$tunnel_network" >/dev/null
docker run -d \
  --name "$fixture_container" \
  --network "$tunnel_network" \
  --network-alias fixture \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=8m \
  -e "OPENLINKER_BROWSER_PROVIDER_LIVE_MARKER=${provider_live_marker}" \
  "$fixture_image" \
  >/dev/null

fixture_url=${OPENLINKER_BROWSER_ACCEPTANCE_FIXTURE_URL:-}
collector_url=${OPENLINKER_BROWSER_ACCEPTANCE_COLLECTOR_URL:-}
fixture_prime_url=${OPENLINKER_BROWSER_ACCEPTANCE_PRIME_URL:-}
rebind_hostname="${prefix}-make-1.1.1.1-rebindfor2m-127.0.0.1-rr-set-1-ttl.1u.ms"
rebind_url="http://${rebind_hostname}/"
browser_architecture=$(
  docker image inspect "$browser_image" --format '{{.Architecture}}'
)
case "$browser_architecture" in
  amd64|arm64) ;;
  *)
    echo "Browser image architecture is unsupported" >&2
    exit 1
    ;;
esac
expected_browser_version=$(
  sed -n \
    "s/^[[:space:]]*\"linux-${browser_architecture}\":[[:space:]]*\"\\([0-9.]*\\)\"[,]\\{0,1\\}[[:space:]]*$/\\1/p" \
    "$repository_root/browser-engine/browser-versions.json"
)
expected_font_sha256=$(tr -d '\n' <"$repository_root/browser-engine/font-contract-v1.sha256")
case "$expected_browser_version" in
  ''|*[!0-9.]*)
    echo "locked Browser version is invalid" >&2
    exit 1
    ;;
esac
case "$expected_font_sha256" in
  *[!0-9a-f]*)
    echo "locked font manifest SHA-256 is invalid" >&2
    exit 1
    ;;
esac
if [ "${#expected_font_sha256}" -ne 64 ]; then
  echo "locked font manifest SHA-256 is invalid" >&2
  exit 1
fi
case "$fixture_prime_url" in
  ""|https://*) ;;
  *)
    echo "temporary HTTPS acceptance fixture prime URL is invalid" >&2
    exit 1
    ;;
esac
case "$fixture_url" in
  "")
    tunnel_attempt=0
    while [ "$tunnel_attempt" -lt 3 ] && [ -z "$fixture_url" ]; do
      docker rm -f "$tunnel_container" >/dev/null 2>&1 || true
      docker run -d \
        --name "$tunnel_container" \
        --network "$tunnel_network" \
        --read-only \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        "$cloudflared_image" \
        tunnel --no-autoupdate --url http://fixture:8080 \
        >/dev/null
      ready_attempt=0
      while [ "$ready_attempt" -lt 35 ]; do
        fixture_url=$(
          docker logs "$tunnel_container" 2>&1 |
            sed -n 's,.*\(https://[-a-z0-9]*\.trycloudflare\.com\).*,\1,p' |
            tail -1
        )
        if [ -n "$fixture_url" ] &&
          docker logs "$tunnel_container" 2>&1 |
            grep -q "Registered tunnel connection"; then
          break
        fi
        fixture_url=
        if [ "$(docker inspect "$tunnel_container" --format '{{.State.Running}}')" != "true" ]; then
          break
        fi
        ready_attempt=$((ready_attempt + 1))
        sleep 1
      done
      tunnel_attempt=$((tunnel_attempt + 1))
      if [ -z "$fixture_url" ] && [ "$tunnel_attempt" -lt 3 ]; then
        sleep $((tunnel_attempt * 20))
      fi
    done
    ;;
  https://*)
    if ! curl -fsS --max-time 10 "$fixture_url/" 2>/dev/null |
      grep -q "OpenLinker Browser Acceptance"; then
      fixture_url=
    fi
    ;;
  *)
    fixture_url=
    ;;
esac
if [ -z "$fixture_url" ]; then
  echo "temporary HTTPS acceptance fixture did not become ready" >&2
  exit 1
fi
fixture_host=${fixture_url#https://}
fixture_host=${fixture_host%%/*}
fixture_host=${fixture_host%%:*}
case "$fixture_host" in
  ''|*[!A-Za-z0-9.-]*)
    echo "temporary HTTPS acceptance fixture hostname is invalid" >&2
    exit 1
    ;;
esac

# cloudflared can announce a tunnel before its public DNS record is visible to
# the exact secure resolver used by the Egress Gateway. Wait for both that
# resolver and the HTTPS route so a propagation race cannot masquerade as an
# SSRF-policy rejection.
fixture_ready_attempt=0
while [ "$fixture_ready_attempt" -lt 30 ]; do
  secure_dns_response=$(
    curl -fsS --max-time 10 \
      -H 'accept: application/dns-json' \
      "https://1.1.1.1/dns-query?name=${fixture_host}&type=A" \
      2>/dev/null || true
  )
  if ! echo "$secure_dns_response" |
    grep -Eq '"Answer":[[:space:]]*\[[^]]*"type":[[:space:]]*1'; then
    secure_dns_response=$(
      curl -fsS --max-time 10 \
        -H 'accept: application/dns-json' \
        "https://1.1.1.1/dns-query?name=${fixture_host}&type=AAAA" \
        2>/dev/null || true
    )
  fi
  if echo "$secure_dns_response" |
    grep -Eq '"Answer":[[:space:]]*\[[^]]*"type":[[:space:]]*(1|28)' &&
    curl -fsS --max-time 10 "$fixture_url/" 2>/dev/null |
      grep -q "OpenLinker Browser Acceptance"; then
    break
  fi
  fixture_ready_attempt=$((fixture_ready_attempt + 1))
  sleep 1
done
if [ "$fixture_ready_attempt" -ge 30 ]; then
  echo "temporary HTTPS acceptance fixture did not propagate to secure DNS" >&2
  exit 1
fi

collector_prevalidated=0
case "$collector_url" in
  "")
    tunnel_attempt=0
    while [ "$tunnel_attempt" -lt 3 ] && [ "$collector_prevalidated" != "1" ]; do
      docker rm -f "$collector_tunnel_container" >/dev/null 2>&1 || true
      docker run -d \
        --name "$collector_tunnel_container" \
        --network "$tunnel_network" \
        --read-only \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        "$cloudflared_image" \
        tunnel --no-autoupdate --url http://fixture:8080 \
        >/dev/null
      ready_attempt=0
      while [ "$ready_attempt" -lt 45 ]; do
        collector_url=$(
          docker logs "$collector_tunnel_container" 2>&1 |
            sed -n 's,.*\(https://[-a-z0-9]*\.trycloudflare\.com\).*,\1,p' |
            tail -1
        )
        if [ -n "$collector_url" ]; then
          candidate_host=${collector_url#https://}
          candidate_host=${candidate_host%%/*}
          candidate_host=${candidate_host%%:*}
          secure_dns_response=$(
            curl -fsS --max-time 5 \
              -H 'accept: application/dns-json' \
              "https://1.1.1.1/dns-query?name=${candidate_host}&type=A" \
              2>/dev/null || true
          )
          if docker logs "$collector_tunnel_container" 2>&1 |
            grep -q "Registered tunnel connection" &&
            echo "$secure_dns_response" |
              grep -Eq '"Answer":[[:space:]]*\[[^]]*"type":[[:space:]]*(1|28)' &&
            curl -fsS --max-time 5 "$collector_url/full/collector" 2>/dev/null |
              grep -q "Unallowlisted Collector Fixture"; then
            collector_prevalidated=1
            break
          fi
        fi
        if [ "$(docker inspect "$collector_tunnel_container" --format '{{.State.Running}}')" != "true" ]; then
          break
        fi
        ready_attempt=$((ready_attempt + 1))
        sleep 1
      done
      tunnel_attempt=$((tunnel_attempt + 1))
      if [ "$collector_prevalidated" != "1" ]; then
        collector_url=
        if [ "$tunnel_attempt" -lt 3 ]; then
          sleep $((tunnel_attempt * 20))
        fi
      fi
    done
    ;;
  https://*)
    if ! curl -fsS --max-time 10 "$collector_url/full/collector" 2>/dev/null |
      grep -q "Unallowlisted Collector Fixture"; then
      collector_url=
    fi
    ;;
  *)
    collector_url=
    ;;
esac
if [ -z "$collector_url" ]; then
  echo "second HTTPS collector fixture did not become ready" >&2
  exit 1
fi
collector_host=${collector_url#https://}
collector_host=${collector_host%%/*}
collector_host=${collector_host%%:*}
case "$collector_host" in
  ''|*[!A-Za-z0-9.-]*)
    echo "second HTTPS collector fixture hostname is invalid" >&2
    exit 1
    ;;
esac
if [ "$collector_host" = "$fixture_host" ]; then
  echo "collector fixture must use a distinct public origin" >&2
  exit 1
fi

collector_ready_attempt=0
while [ "$collector_prevalidated" != "1" ] && [ "$collector_ready_attempt" -lt 30 ]; do
  secure_dns_response=$(
    curl -fsS --max-time 10 \
      -H 'accept: application/dns-json' \
      "https://1.1.1.1/dns-query?name=${collector_host}&type=A" \
      2>/dev/null || true
  )
  if ! echo "$secure_dns_response" |
    grep -Eq '"Answer":[[:space:]]*\[[^]]*"type":[[:space:]]*1'; then
    secure_dns_response=$(
      curl -fsS --max-time 10 \
        -H 'accept: application/dns-json' \
        "https://1.1.1.1/dns-query?name=${collector_host}&type=AAAA" \
        2>/dev/null || true
    )
  fi
  if echo "$secure_dns_response" |
    grep -Eq '"Answer":[[:space:]]*\[[^]]*"type":[[:space:]]*(1|28)' &&
    curl -fsS --max-time 10 "$collector_url/full/collector" 2>/dev/null |
      grep -q "Unallowlisted Collector Fixture"; then
    break
  fi
  collector_ready_attempt=$((collector_ready_attempt + 1))
  sleep 1
done
if [ "$collector_prevalidated" != "1" ] && [ "$collector_ready_attempt" -ge 30 ]; then
  echo "second HTTPS collector fixture did not propagate to secure DNS" >&2
  exit 1
fi

docker network create --internal "$internal_network" >/dev/null
docker network create "$public_network" >/dev/null
docker volume create "$control_volume" >/dev/null
docker volume create "$key_volume" >/dev/null
docker volume create "$state_volume" >/dev/null
docker volume create "$capture_volume" >/dev/null

docker run --rm \
  --network "$public_network" \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --entrypoint node \
  "$observer_image" \
  -e '
    const https = require("node:https");
    const host = process.argv[1];
    const request = https.get(
      {
        hostname: "1.1.1.1",
        path: `/dns-query?name=${encodeURIComponent(host)}&type=A`,
        headers: { accept: "application/dns-json" },
      },
      (response) => {
        let raw = "";
        response.on("data", (chunk) => { raw += chunk; });
        response.on("end", () => {
          try {
            const body = JSON.parse(raw);
            const publicPhase = body.Answer?.some(
              (answer) => answer.type === 1 && answer.data === "1.1.1.1",
            );
            process.exit(publicPhase ? 0 : 1);
          } catch {
            process.exit(1);
          }
        });
      },
    );
    request.setTimeout(10_000, () => request.destroy());
    request.on("error", () => process.exit(1));
  ' \
  "$rebind_hostname"
sleep 2

docker run -d \
  --name "$egress_container" \
  --network "$internal_network" \
  --network-alias openlinker-egress-gateway \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  -e OPENLINKER_EGRESS_LISTEN=0.0.0.0:3128 \
  -e OPENLINKER_EGRESS_DOH_URL=https://1.1.1.1/dns-query \
  "$egress_image" \
  >/dev/null
docker network connect "$public_network" "$egress_container"
egress_internal_ip=$(
  docker inspect "$egress_container" \
    --format "{{(index .NetworkSettings.Networks \"${internal_network}\").IPAddress}}"
)
case "$egress_internal_ip" in
  ''|*[!0-9.]*)
    echo "Egress Gateway internal address is invalid" >&2
    exit 1
    ;;
esac

docker run -d \
  --name "$runtime_container" \
  --network "$internal_network" \
  --read-only \
  --user 10001:10001 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /browser-home:rw,noexec,nosuid,nodev,size=64m,uid=10001,gid=10001,mode=0700 \
  --tmpfs /browser-tmp:rw,noexec,nosuid,nodev,size=512m,uid=10001,gid=10001,mode=0700 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m,uid=10001,gid=10001,mode=0700 \
  --shm-size 1g \
  --mount "type=volume,src=${control_volume},dst=/browser-control" \
  --mount "type=volume,src=${key_volume},dst=/browser-key" \
  --mount "type=volume,src=${state_volume},dst=/browser-state" \
  -e OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE=/browser-control/channel-credential \
  -e "OPENLINKER_BROWSER_EGRESS_PROXY=http://${egress_internal_ip}:3128" \
  -e OPENLINKER_BROWSER_ACTIVE_LEASE_FILE=/browser-control/leases/active-lease.json \
  -e OPENLINKER_BROWSER_SOCKET=/browser-control/openlinker.browser.sock \
  -e OPENLINKER_BROWSER_PROFILE_DIR=/browser-tmp/profiles/active \
  -e OPENLINKER_BROWSER_PROFILE_STORE=/browser-state/encrypted-profiles \
  -e OPENLINKER_BROWSER_PROFILE_WORK_ROOT=/browser-tmp/profiles \
  -e OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE=/browser-key/profile-root-key \
  -e DEBUG=pw:browser \
  "$browser_image" \
  >/dev/null

runtime_network_count=$(
  docker inspect "$runtime_container" --format '{{len .NetworkSettings.Networks}}'
)
runtime_ports=$(
  docker inspect "$runtime_container" --format '{{json .HostConfig.PortBindings}}'
)
internal_network_flag=$(
  docker network inspect "$internal_network" --format '{{.Internal}}'
)
if [ "$runtime_network_count" != "1" ] ||
  [ "$runtime_ports" != "{}" ] ||
  [ "$internal_network_flag" != "true" ]; then
  echo "Browser Runtime network or port isolation is invalid" >&2
  exit 1
fi

docker run -d \
  --name "$observer_container" \
  --network "container:${runtime_container}" \
  --read-only \
  --cap-drop ALL \
  --cap-add CHOWN \
  --cap-add NET_RAW \
  --cap-add SETGID \
  --cap-add SETUID \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=8m \
  --mount "type=volume,src=${capture_volume},dst=/capture" \
  "$observer_image" \
  -Z root -i any -nn -U -w /capture/runtime.pcap "tcp or udp" \
  >/dev/null

observer_ready_attempt=0
while [ "$observer_ready_attempt" -lt 20 ]; do
  if docker logs "$observer_container" 2>&1 | grep -q "listening on any"; then
    break
  fi
  observer_ready_attempt=$((observer_ready_attempt + 1))
  sleep 1
done
if [ "$observer_ready_attempt" -ge 20 ]; then
  echo "Browser network-namespace observer did not become ready" >&2
  docker logs "$observer_container" >&2 || true
  exit 1
fi

full_result=$(docker run --rm \
  --network "$internal_network" \
  --user 10001:10001 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --mount "type=volume,src=${control_volume},dst=/browser-control" \
  "$client_image" \
  --mode mcp-evidence \
  --expected-browser-version "$expected_browser_version" \
  --expected-font-sha256 "$expected_font_sha256"

docker run --rm \
  --network "$internal_network" \
  --user 10001:10001 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --mount "type=volume,src=${control_volume},dst=/browser-control" \
  "$client_image" \
  --mode full \
  --public-url "$fixture_url/" \
  --collector-url "$collector_url/" \
  --prime-url "$fixture_prime_url" \
  --rebind-url "$rebind_url" \
  --expected-browser-version "$expected_browser_version" \
  --expected-font-sha256 "$expected_font_sha256")
printf '%s\n' "$full_result"
if ! printf '%s\n' "$full_result" |
  grep -q '"action":"read_only_semantic_observation"'; then
  echo "Browser action-latency comparison is missing" >&2
  exit 1
fi
for latency_field in samples p50_ms p95_ms p99_ms; do
  latency_field_count=$(printf '%s\n' "$full_result" |
    grep -o "\"${latency_field}\":" |
    wc -l |
    tr -d ' ')
  if [ "$latency_field_count" != "2" ]; then
    echo "Browser action-latency field ${latency_field} is incomplete" >&2
    exit 1
  fi
done
sample_count=$(printf '%s\n' "$full_result" |
  grep -o '"samples":40' |
  wc -l |
  tr -d ' ')
if [ "$sample_count" != "2" ]; then
  echo "Browser action-latency sample count is invalid" >&2
  exit 1
fi

metrics=$(curl -fsS --max-time 10 "$fixture_url/metrics")
for expected in \
	'"post_requests":0' \
	'"full_mutation_requests":0' \
	'"star_requests":1' \
	'"starred":true' \
	'"method_post_requests":1' \
	'"method_put_requests":1' \
	'"method_patch_requests":1' \
	'"method_delete_requests":1' \
	'"form_submit_requests":1' \
	'"websocket_state_updates":1' \
	'"collector_mutations":0' \
	'"collector_websockets":0' \
	'"child_mutations":1' \
	'"response_loss_requests":1' \
	'"service_worker_requests":0' \
	'"websocket_requests":0' \
  '"search_requests":1' \
  '"access_denied_requests":3' \
  '"rate_limited_requests":1'; do
  echo "$metrics" | grep -q "$expected" || {
    echo "Browser emitted a forbidden fixture request" >&2
    exit 1
  }
done

if [ "${OPENLINKER_BROWSER_ACCEPTANCE_LIVE_PROVIDER_MATRIX:-0}" = "1" ]; then
  OPENLINKER_BROWSER_LIVE_FIXTURE_URL="$fixture_url" \
    OPENLINKER_BROWSER_LIVE_FIXTURE_MARKER="$provider_live_marker" \
    OPENLINKER_BROWSER_LIVE_INTERNAL_NETWORK="$internal_network" \
    OPENLINKER_BROWSER_LIVE_CONTROL_VOLUME="$control_volume" \
    OPENLINKER_BROWSER_LIVE_EGRESS_IP="$egress_internal_ip" \
    OPENLINKER_BROWSER_LIVE_PREFIX="${prefix}-provider" \
    "$repository_root/test/browser-provider-live/matrix.sh"
fi

docker stop -t 5 "$egress_container" >/dev/null
docker run --rm \
  --network "$internal_network" \
  --user 10001:10001 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --mount "type=volume,src=${control_volume},dst=/browser-control" \
  "$client_image" \
  --mode gateway-down \
  --public-url "$fixture_url/"

docker exec "$runtime_container" node -e '
  const fs = require("node:fs");
  const commands = [];
  for (const name of fs.readdirSync("/proc")) {
    if (!/^[0-9]+$/.test(name)) continue;
    try {
      const command = fs
        .readFileSync(`/proc/${name}/cmdline`)
        .toString()
        .split("\0")
        .filter(Boolean);
      if (command.some((value) => /(?:chromium|chrome)/i.test(value))) {
        commands.push(command);
      }
    } catch {}
  }
  const required = [
    "--disable-quic",
    "--dns-over-https-mode=off",
    "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
  ];
  for (const flag of required) {
    if (!commands.some((command) => command.includes(flag))) {
      throw new Error(`missing Chromium flag ${flag}`);
    }
  }
  const forbiddenRemoteDebugging = commands.flatMap((command) =>
    command.filter(
      (argument) =>
        argument.startsWith("--remote-debugging-") &&
        argument !== "--remote-debugging-pipe",
    ),
  );
  if (forbiddenRemoteDebugging.length !== 0) {
    throw new Error(
      `Browser exposed a forbidden remote debugging surface: ${forbiddenRemoteDebugging.join(",")}`,
    );
  }
'

docker stop -t 5 "$observer_container" >/dev/null

if ! docker run --rm \
  --user tcpdump:tcpdump \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --mount "type=volume,src=${capture_volume},dst=/capture,readonly" \
  "$observer_image" \
  -nn -r /capture/runtime.pcap -c 1 \
  "host ${egress_internal_ip} and tcp port 3128" \
  2>/dev/null |
  grep -q .; then
  echo "Browser packet observer did not capture the required Gateway traffic" >&2
  docker logs "$observer_container" >&2 || true
  exit 1
fi

if docker run --rm \
  --user tcpdump:tcpdump \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --mount "type=volume,src=${capture_volume},dst=/capture,readonly" \
  "$observer_image" \
  -nn -r /capture/runtime.pcap -c 1 udp \
  2>/dev/null |
  grep -q .; then
  echo "Chromium emitted direct UDP/QUIC/DNS/WebRTC traffic" >&2
  exit 1
fi

if docker run --rm \
  --user tcpdump:tcpdump \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --mount "type=volume,src=${capture_volume},dst=/capture,readonly" \
  "$observer_image" \
  -nn -r /capture/runtime.pcap -c 1 \
  "tcp and not (host ${egress_internal_ip} and port 3128)" \
  2>/dev/null |
  grep -q .; then
  echo "Chromium emitted TCP traffic outside the Egress Gateway" >&2
  exit 1
fi

docker exec "$runtime_container" node -e '
  const net = require("node:net");
  let done = false;
  const finish = (code) => {
    if (done) return;
    done = true;
    process.exit(code);
  };
  const socket = net.connect({ host: "1.1.1.1", port: 443 });
  socket.once("connect", () => finish(1));
  socket.once("error", () => finish(0));
  setTimeout(() => finish(0), 2000);
'
docker exec "$runtime_container" node -e '
  const dgram = require("node:dgram");
  let done = false;
  const socket = dgram.createSocket("udp4");
  const finish = (code) => {
    if (done) return;
    done = true;
    socket.close();
    process.exit(code);
  };
  socket.once("message", () => finish(1));
  socket.once("error", () => finish(0));
  socket.send(
    Buffer.from([0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 97, 0, 0, 1, 0, 1]),
    53,
    "1.1.1.1",
  );
  setTimeout(() => finish(0), 2000);
'

echo "Browser image acceptance passed; packet evidence: Gateway TCP observed, UDP=0, non-Gateway TCP=0; forbidden fixture requests: $metrics"
