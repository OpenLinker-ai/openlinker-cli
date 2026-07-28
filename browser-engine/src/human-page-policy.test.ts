import assert from "node:assert/strict";
import test from "node:test";

import type { WebSocketRoute } from "playwright-core";

import {
  restoreAgentPagePolicy,
  routePageWebSocket,
  type ControlState,
} from "./engine.js";

function socket(options?: { closeError?: Error }) {
  let connected = 0;
  let closed = 0;
  const value = {
    connectToServer() {
      connected += 1;
      return value;
    },
    async close() {
      closed += 1;
      if (options?.closeError) throw options.closeError;
    },
  } as unknown as WebSocketRoute;
  return {
    value,
    connected: () => connected,
    closed: () => closed,
  };
}

function state(controller: "agent" | "human"): ControlState {
  return {
    controller,
    attachmentKey: controller === "human" ? "attachment" : "",
    pageWebSockets: new Set(),
  };
}

test("page WebSockets connect only for the human controller epoch", async () => {
  const human = state("human");
  const admitted = socket();
  await routePageWebSocket(human, admitted.value);
  assert.equal(admitted.connected(), 1);
  assert.equal(admitted.closed(), 0);
  assert.equal(human.pageWebSockets.size, 1);

  const agent = state("agent");
  const rejected = socket();
  await routePageWebSocket(agent, rejected.value);
  assert.equal(rejected.connected(), 0);
  assert.equal(rejected.closed(), 1);
  assert.equal(agent.pageWebSockets.size, 0);
});

test("release closes human WebSockets before restoring Agent policy", async () => {
  const control = state("human");
  const first = socket();
  const second = socket();
  control.pageWebSockets.add(first.value);
  control.pageWebSockets.add(second.value);

  await restoreAgentPagePolicy(control);
  assert.equal(first.closed(), 1);
  assert.equal(second.closed(), 1);
  assert.equal(control.pageWebSockets.size, 0);
  assert.equal(control.controller, "agent");
  assert.equal(control.attachmentKey, "");
});

test("WebSocket close failure never admits the Agent", async () => {
  const control = state("human");
  const failing = socket({ closeError: new Error("close failed") });
  control.pageWebSockets.add(failing.value);

  await assert.rejects(restoreAgentPagePolicy(control), /close failed/);
  assert.equal(control.controller, "human");
  assert.equal(control.attachmentKey, "attachment");
});
