package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

type counters struct {
	postRequests          atomic.Uint64
	webSocketRequests     atomic.Uint64
	serviceWorkerRequests atomic.Uint64
	searchRequests        atomic.Uint64
	accessDeniedRequests  atomic.Uint64
	rateLimitedRequests   atomic.Uint64
}

func main() {
	var observed counters
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; worker-src 'self'")
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(response, fixtureHTML)
	})
	mux.HandleFunc("/state-change", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			observed.postRequests.Add(1)
		}
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/socket", func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Upgrade") != "" {
			observed.webSocketRequests.Add(1)
		}
		http.Error(response, "WebSocket fixture must not be reached", http.StatusUpgradeRequired)
	})
	mux.HandleFunc("/sw.js", func(response http.ResponseWriter, _ *http.Request) {
		observed.serviceWorkerRequests.Add(1)
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(response, "self.addEventListener('fetch', () => {});")
	})
	mux.HandleFunc("/redirect-private", func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "http://127.0.0.1/", http.StatusFound)
	})
	mux.HandleFunc("/search", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "GET required", http.StatusMethodNotAllowed)
			return
		}
		observed.searchRequests.Add(1)
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(
			response,
			"<!doctype html><html><head><title>OpenLinker Search Result</title></head><body><p id=\"query\">query=%s</p></body></html>",
			html.EscapeString(request.URL.Query().Get("q")),
		)
	})
	mux.HandleFunc("/plain", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(response, plainHTML)
	})
	mux.HandleFunc("/access-denied", func(response http.ResponseWriter, _ *http.Request) {
		observed.accessDeniedRequests.Add(1)
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.WriteHeader(http.StatusForbidden)
		fmt.Fprint(response, accessDeniedHTML)
	})
	mux.HandleFunc("/rate-limited", func(response http.ResponseWriter, _ *http.Request) {
		observed.rateLimitedRequests.Add(1)
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Retry-After", "2")
		response.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(response, rateLimitedHTML)
	})
	mux.HandleFunc("/challenge-suspected", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(response, suspectedChallengeHTML)
	})
	mux.HandleFunc("/challenge-required", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(
			"Content-Security-Policy",
			"default-src 'none'; frame-src https://challenges.cloudflare.com",
		)
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(response, requiredChallengeHTML)
	})
	mux.HandleFunc("/metrics", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]uint64{
			"post_requests":           observed.postRequests.Load(),
			"websocket_requests":      observed.webSocketRequests.Load(),
			"service_worker_requests": observed.serviceWorkerRequests.Load(),
			"search_requests":         observed.searchRequests.Load(),
			"access_denied_requests":  observed.accessDeniedRequests.Load(),
			"rate_limited_requests":   observed.rateLimitedRequests.Load(),
		})
	})
	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

const fixtureHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>OpenLinker Browser Acceptance</title></head>
<body>
  <main>
    <h1>OpenLinker Browser Acceptance</h1>
    <form action="/search" method="get" role="search">
      <label for="fixture-search">Public search</label>
      <input
        id="fixture-search"
        name="q"
        type="search"
        style="position:fixed;left:160px;top:120px;width:420px;height:48px"
      >
    </form>
    <p id="pointerdown-count">pointerdown=0</p>
    <p id="mousedown-count">mousedown=0</p>
    <p id="click-count">click=0</p>
    <p id="post">post=pending</p>
    <p id="websocket">websocket=pending</p>
    <p id="service-worker">service_worker=pending</p>
    <p id="webrtc">webrtc_probe=pending</p>
    <p id="webtransport">webtransport_probe=pending</p>
    <a href="/second-page">Safe public link</a>
  </main>
  <script>
    const set = (id, value) => { document.getElementById(id).textContent = value; };
    const search = document.getElementById("fixture-search");
    for (const eventName of ["pointerdown", "mousedown", "click"]) {
      let count = 0;
      search.addEventListener(eventName, () => {
        count++;
        set(eventName + "-count", eventName + "=" + count);
      });
    }
    fetch("/state-change", { method: "POST", body: "must-not-leave-browser" })
      .then(() => set("post", "post=allowed"))
      .catch(() => set("post", "post=blocked"));
    try {
      const socket = new WebSocket(
        (location.protocol === "https:" ? "wss://" : "ws://") +
        location.host + "/socket"
      );
      let settled = false;
      const finish = (value) => {
        if (settled) return;
        settled = true;
        set("websocket", value);
        socket.close();
      };
      socket.onopen = () => finish("websocket=allowed");
      socket.onerror = () => finish("websocket=blocked");
      socket.onclose = () => finish("websocket=blocked");
      setTimeout(() => finish("websocket=blocked"), 1500);
    } catch {
      set("websocket", "websocket=blocked");
    }
    if (!("serviceWorker" in navigator)) {
      set("service-worker", "service_worker=blocked");
    } else {
      let settled = false;
      const finish = (value) => {
        if (settled) return;
        settled = true;
        set("service-worker", value);
      };
      navigator.serviceWorker.register("/sw.js")
        .then((registration) => {
          setTimeout(() => finish(
            registration.installing || registration.waiting || registration.active
              ? "service_worker=allowed"
              : "service_worker=blocked"
          ), 500);
        })
        .catch(() => finish("service_worker=blocked"));
      setTimeout(() => finish("service_worker=blocked"), 1500);
    }
    if (!("RTCPeerConnection" in window)) {
      set("webrtc", "webrtc_probe=unsupported");
    } else {
      try {
        const peer = new RTCPeerConnection({
          iceServers: [{ urls: "stun:1.1.1.1:3478" }],
        });
        peer.createDataChannel("openlinker-no-bypass-probe");
        set("webrtc", "webrtc_probe=started");
        peer.createOffer()
          .then((offer) => peer.setLocalDescription(offer))
          .catch(() => {});
        setTimeout(() => {
          peer.close();
          set("webrtc", "webrtc_probe=complete");
        }, 1500);
      } catch {
        set("webrtc", "webrtc_probe=constructor_failed");
      }
    }
    if (!("WebTransport" in window)) {
      set("webtransport", "webtransport_probe=unsupported");
    } else {
      try {
        const transport = new WebTransport(
          location.origin + "/webtransport-no-bypass-probe"
        );
        set("webtransport", "webtransport_probe=started");
        transport.ready.catch(() => {});
        transport.closed.catch(() => {});
        setTimeout(() => {
          try { transport.close(); } catch {}
          set("webtransport", "webtransport_probe=complete");
        }, 1500);
      } catch {
        set("webtransport", "webtransport_probe=constructor_failed");
      }
    }
  </script>
</body>
</html>`

const plainHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>OpenLinker Plain Document</title></head>
<body>
  <label for="plain-input">Plain input</label>
  <input id="plain-input" type="text"
    style="position:fixed;left:160px;top:120px;width:420px;height:48px">
</body>
</html>`

const accessDeniedHTML = `<!doctype html>
<html lang="en"><head><title>Access denied fixture</title></head>
<body><p>ordinary access denial</p></body></html>`

const rateLimitedHTML = `<!doctype html>
<html lang="en"><head><title>Rate limited fixture</title></head>
<body><p>retry later</p></body></html>`

const suspectedChallengeHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Suspected challenge fixture</title></head>
<body>
  <div id="captcha-probe">ambiguous challenge marker</div>
  <label for="challenge-input">Challenge input</label>
  <input id="challenge-input" type="text"
    style="position:fixed;left:160px;top:120px;width:420px;height:48px">
  <p id="same-document-history">same_document_history=pending</p>
  <p id="pageshow-state">pageshow_persisted=pending</p>
  <script>
    history.pushState({}, "", "/challenge-suspected?same-document=1");
    document.getElementById("same-document-history").textContent =
      "same_document_history=advanced";
    window.addEventListener("pageshow", (event) => {
      document.getElementById("pageshow-state").textContent =
        "pageshow_persisted=" + String(event.persisted);
    });
  </script>
</body>
</html>`

const requiredChallengeHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Required challenge fixture</title></head>
<body>
  <iframe
    title="Turnstile challenge"
    src="https://challenges.cloudflare.com/turnstile/openlinker-fixture">
  </iframe>
</body>
</html>`
