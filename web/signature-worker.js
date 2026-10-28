"use strict";
importScripts("network.js", "wasm_exec.js");
let ready;
async function initialize(ref) {
  if (!ref || ref.size > (16 << 20)) throw new Error("Invalid signature verifier");
  const binary = await sprFetch("signatures.wasm", 16 << 20, ref.size);
  if (await sprSHA256(binary) !== ref.sha256) throw new Error("Signature verifier checksum mismatch");
  const go = new Go();
  go.argv = ["spr-signatures-wasm"];
  const { instance } = await WebAssembly.instantiate(binary, go.importObject);
  await new Promise((resolve, reject) => {
    self.sprSignaturesReady = resolve;
    go.run(instance).then(() => reject(new Error("Signature verifier stopped")), reject);
  });
}
self.onmessage = async ({ data }) => {
  try {
    ready ||= initialize(data.ref);
    await ready;
    const valid = typeof data.publicKey === "string" && data.publicKey.length <= 16384 &&
      typeof data.message === "string" && data.message.length <= 262144 &&
      self.sprVerifySignature(data.publicKey, data.kind, data.manifest, data.message);
    self.postMessage({ id: data.id, valid: Boolean(valid) });
  } catch { self.postMessage({ id: data.id, valid: false }); }
};
