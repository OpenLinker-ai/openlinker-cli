import { once } from "node:events";

import { BrowserEngine } from "./engine.js";
import {
  failure,
  parseRequest,
  type EngineResponse,
} from "./protocol.js";

const MAX_INPUT_BYTES = 256 * 1024;

async function main(): Promise<void> {
  const engine = await BrowserEngine.create(process.env);
  const shutdown = async (): Promise<void> => {
    await engine.close().catch(() => undefined);
    process.exit(0);
  };
  process.once("SIGINT", () => {
    void shutdown();
  });
  process.once("SIGTERM", () => {
    void shutdown();
  });

  for await (const line of boundedLines(process.stdin, MAX_INPUT_BYTES)) {
    let response: EngineResponse;
    try {
      const request = parseRequest(line);
      response = await engine.execute(request);
    } catch (error) {
      response = failure(
        extractActionID(line),
        "BROWSER_OUTPUT_INVALID",
        "browser engine request is invalid",
        false,
      );
    }
    await writeResponse(response);
  }
  await engine.close();
}

async function* boundedLines(
  input: NodeJS.ReadableStream,
  maximumBytes: number,
): AsyncGenerator<string> {
  let buffered = Buffer.alloc(0);
  for await (const rawChunk of input) {
    const chunk = Buffer.isBuffer(rawChunk)
      ? rawChunk
      : Buffer.from(rawChunk as string);
    buffered = Buffer.concat([buffered, chunk]);
    for (;;) {
      const newline = buffered.indexOf(0x0a);
      if (newline < 0) {
        if (buffered.byteLength > maximumBytes) {
          throw new Error("browser engine input exceeds the line limit");
        }
        break;
      }
      if (newline > maximumBytes) {
        throw new Error("browser engine input exceeds the line limit");
      }
      const line = buffered.subarray(0, newline).toString("utf8");
      buffered = buffered.subarray(newline + 1);
      yield line;
    }
  }
  if (buffered.byteLength !== 0) {
    throw new Error("browser engine input ended without a newline");
  }
}

function extractActionID(line: string): string {
  try {
    const value: unknown = JSON.parse(line);
    if (
      value !== null &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      "action_id" in value &&
      typeof value.action_id === "string" &&
      /^[1-9][0-9]*$/.test(value.action_id) &&
      value.action_id.length <= 32
    ) {
      return value.action_id;
    }
  } catch {
    // The Go supervisor rejects action_id 0 and restarts the engine.
  }
  return "0";
}

async function writeResponse(response: EngineResponse): Promise<void> {
  const raw = `${JSON.stringify(response)}\n`;
  if (!process.stdout.write(raw)) {
    await once(process.stdout, "drain");
  }
}

void main().catch(() => {
  process.exit(1);
});
