let build, worker, sequence = 0;
const pending = new Map(), keys = new Map(), checked = new Set();
export function configureSignatures(value) { build = value; }
export function setSigningKey(manifest, publicKey) { keys.set(manifest, publicKey); }
function stop() {
  worker?.terminate(); worker = null;
  for (const p of pending.values()) { clearTimeout(p.timer); p.reject(new Error("Signature verifier unavailable")); }
  pending.clear();
}
export async function verifyMessage(kind, manifest, value) {
  const publicKey = keys.get(manifest);
  if (!publicKey) throw new Error("Coordinator signing key is missing");
  const message = JSON.stringify(value);
  if (message.length > 262144) throw new Error("Signed message exceeds limit");
  const cacheKey = `${kind}:${manifest}:${await sprSHA256(new TextEncoder().encode(message))}`;
  if (checked.has(cacheKey)) return;
  if (!build?.assets?.["signatures.wasm"]) throw new Error("Signature verifier is missing");
  if (!worker) {
    worker = new Worker("signature-worker.js");
    worker.onerror = stop;
    worker.onmessage = ({ data }) => {
      const p = pending.get(data.id); if (!p) return;
      clearTimeout(p.timer); pending.delete(data.id);
      if (data.valid === true) p.resolve(); else p.reject(new Error("Invalid coordinator signature"));
    };
  }
  await new Promise((resolve, reject) => {
    const id = ++sequence;
    pending.set(id, { resolve, reject, timer: setTimeout(stop, 30000) });
    worker.postMessage({ id, ref: build.assets["signatures.wasm"], publicKey, kind, manifest, message });
  });
  if (checked.size >= 4096) checked.clear();
  checked.add(cacheKey);
}
window.addEventListener("pagehide", stop);
