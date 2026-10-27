"use strict";
importScripts("network.js", "wasm_exec.js");

let running = false;
self.onmessage = async ({ data }) => {
  if (running) return;
  running = true;
  let instance;
  let reported = false;
  const memory = () => instance ? instance.exports.mem.buffer.byteLength : 0;
  self.sprFailure = error => {
    reported = true;
    self.postMessage({ type: "error", error: String(error), wasm_memory_bytes: memory() });
  };
  try {
    if (!self.isSecureContext || typeof crypto.getRandomValues !== "function" || !crypto.subtle) {
      throw new Error("This page requires HTTPS or localhost and browser cryptographic randomness");
    }
    const ref = data.build.assets["contributor.wasm"];
    self.postMessage({ type: "stage", stage: "load_engine" });
    const binary = await sprFetch("contributor.wasm", 64 << 20, ref.size);
    if (await sprSHA256(binary) !== ref.sha256) throw new Error("WebAssembly download checksum mismatch");
    const go = new Go();
    go.argv = ["spr-browser-wasm"];
    ({ instance } = await WebAssembly.instantiate(binary, go.importObject));
    self.sprFixture = data.fixtureText;
    self.sprInput = new Uint8Array(data.input);
    self.sprStage = text => self.postMessage({ type: "stage", ...JSON.parse(text), wasm_memory_bytes: memory() });
    self.sprSuccess = (text, output) => {
      reported = true;
      const report = { ...JSON.parse(text), wasm_memory_bytes: memory() };
      self.postMessage({ type: "complete", report, output: output.buffer }, [output.buffer]);
    };
    await go.run(instance);
    if (!reported) throw new Error("Contribution engine exited without a result");
  } catch (error) {
    self.sprFailure(error.message || String(error));
  }
  self.close();
};
