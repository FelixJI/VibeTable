"""Embedded Node bootstrap for the fail-closed plugin Worker subprocess.

Keeping the bootstrap in a Python module ensures PyInstaller includes it in the
backend executable.  Plugin source is still sent over stdin and never resolved
relative to the process working directory.
"""

from __future__ import annotations

RUNNER_SOURCE = r""""use strict";

const readline = require("node:readline");
const vm = require("node:vm");

const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const iterator = lines[Symbol.asyncIterator]();
let nextCapabilityId = 1;

async function readMessage() {
  const item = await iterator.next();
  if (item.done) throw new Error("plugin host closed the protocol");
  return JSON.parse(item.value);
}

function send(message) {
  process.stdout.write(`${JSON.stringify(message)}\n`);
}

async function capability(name, args) {
  const id = nextCapabilityId++;
  send({ type: "capability", id, name, args });
  const response = await readMessage();
  if (response.type !== "capabilityResult" || response.id !== id) {
    throw new Error("invalid plugin capability response");
  }
  if (!response.ok) throw new Error(response.error || "plugin capability failed");
  return response.value;
}

async function main() {
  const request = await readMessage();
  if (request.type !== "invoke") throw new Error("invalid worker invocation");
  const sandbox = Object.create(null);
  // Inject the host bridge only long enough to capture it in closures created
  // inside the plugin realm. Passing host-realm functions or JSON objects
  // directly to plugin code would expose their Function constructor.
  sandbox.__hostCapability = capability;
  sandbox.__payloadJson = JSON.stringify(request.payload);
  const context = vm.createContext(sandbox, {
    name: "vibetable-plugin-worker",
    codeGeneration: { strings: false, wasm: false },
  });
  vm.runInContext(`
    (() => {
      const hostCapability = globalThis.__hostCapability;
      const clone = (value) => value === undefined
        ? undefined
        : JSON.parse(JSON.stringify(value));
      const call = async (name, args) => clone(await hostCapability(name, clone(args)));
      const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
      const decodeBase64 = (encoded) => {
        const clean = encoded.replace(/=+$/, "");
        const bytes = [];
        let buffer = 0;
        let bits = 0;
        for (const character of clean) {
          const value = base64Alphabet.indexOf(character);
          if (value < 0) throw new Error("host returned invalid base64 file content");
          buffer = (buffer << 6) | value;
          bits += 6;
          if (bits >= 8) {
            bits -= 8;
            bytes.push((buffer >> bits) & 255);
          }
        }
        return new Uint8Array(bytes);
      };
      const encodeBase64 = (content) => {
        if (!(content instanceof Uint8Array)) throw new Error("file content must be Uint8Array");
        let encoded = "";
        for (let index = 0; index < content.length; index += 3) {
          const first = content[index];
          const second = content[index + 1];
          const third = content[index + 2];
          const value = (first << 16) | ((second || 0) << 8) | (third || 0);
          encoded += base64Alphabet[(value >> 18) & 63];
          encoded += base64Alphabet[(value >> 12) & 63];
          encoded += second === undefined ? "=" : base64Alphabet[(value >> 6) & 63];
          encoded += third === undefined ? "=" : base64Alphabet[value & 63];
        }
        return encoded;
      };
      const pickRead = async (options) => {
        const descriptor = await call("file.pickRead", options || {});
        if (descriptor === null) return null;
        return Object.freeze({
          ...descriptor,
          read: async () => decodeBase64((await call("file.read", { grantId: descriptor.grantId })).base64),
        });
      };
      const pickWrite = async (options) => {
        const descriptor = await call("file.pickWrite", options);
        if (descriptor === null) return null;
        return Object.freeze({
          ...descriptor,
          write: (content) => call("file.write", {
            grantId: descriptor.grantId,
            base64: encodeBase64(content),
          }),
        });
      };
      const api = {
        data: {
          read: (request) => call("data.read", request),
          mutate: (plan) => call("data.mutate", plan),
        },
        file: {
          pickRead,
          pickWrite,
        },
        storage: {
          get: (key) => call("storage.private.get", { key }),
          set: (key, value) => call("storage.private.set", { key, value }),
          delete: (key) => call("storage.private.delete", { key }),
        },
        ui: {
          emitResult: (result) => call("ui.emitResult", result),
          reportProgress: (progress) => call("ui.reportProgress", progress),
        },
        context: { read: () => call("context.read", {}) },
      };
      for (const group of Object.values(api)) Object.freeze(group);
      globalThis.__capabilities = Object.freeze(api);
      globalThis.__payload = JSON.parse(globalThis.__payloadJson);
      globalThis.__signal = Object.freeze({
        aborted: false,
        reason: undefined,
        throwIfAborted() {},
        addEventListener() {},
        removeEventListener() {},
      });
      delete globalThis.__hostCapability;
      delete globalThis.__payloadJson;
    })();
  `, context);
  const module = new vm.SourceTextModule(request.source, {
    context,
    identifier: "vibetable-plugin://worker.mjs",
  });
  await module.link(() => {
    throw new Error("plugin module imports are disabled; bundle Worker dependencies first");
  });
  await module.evaluate();
  const method = module.namespace[request.method];
  if (typeof method !== "function") {
    throw new Error(`plugin Worker does not export ${request.method}()`);
  }
  const value = await method(sandbox.__payload, sandbox.__capabilities, sandbox.__signal);
  send({ type: "result", value });
}

main().catch((error) => {
  send({ type: "error", error: error instanceof Error ? error.message : String(error) });
  process.exitCode = 1;
});
"""

__all__ = ["RUNNER_SOURCE"]
