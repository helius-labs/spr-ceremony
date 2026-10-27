// Public metadata checks shared by queue, recovery, and transfer paths.
export const FORMAT = "spr-ceremony-v1";
export const ENGINE = "gnark-v0.16.4-bn254-mpcsetup";
export const MAX_INPUT = 128 << 20;
export const MAX_JSON = 4 << 20;
export const hex = value => typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
export const id = value => typeof value === "string" && /^[a-z0-9][a-z0-9_-]{0,99}$/.test(value);
export function require(condition, message) { if (!condition) throw new Error(message); }
export function artifact(a, limit = MAX_INPUT + 32) {
  require(a && hex(a.sha256) && Number.isSafeInteger(a.size) && a.size > 0 && a.size <= limit, "Invalid or oversized artifact reference");
  return a;
}
export const sameArtifact = (a, b) => a && b && a.sha256 === b.sha256 && a.size === b.size;
export function equal(a, b) {
  if (a === b) return true;
  if (!a || !b || typeof a !== "object" || typeof b !== "object" || Array.isArray(a) !== Array.isArray(b)) return false;
  const keys = Object.keys(a).sort();
  return keys.length === Object.keys(b).length && keys.every(k => Object.hasOwn(b, k) && equal(a[k], b[k]));
}
export function shapeSize(shape) {
  require(shape && Array.isArray(shape.commitments ?? []) && (shape.commitments?.length ?? 0) <= 255, "Invalid commitment shape");
  const sizes = [shape.private, shape.z, ...(shape.commitments ?? [])];
  require(sizes.every(n => Number.isSafeInteger(n) && n >= 0 && n <= MAX_INPUT / 32), "Invalid parameter vector size");
  const size = 203 + 32 * sizes.reduce((a, b) => a + b, 0) + 164 * (shape.commitments?.length ?? 0);
  require(size <= MAX_INPUT, "Circuit exceeds browser limits");
  return size;
}
export function checkJob(job, invitation) {
  const scope = invitation.jobs.find(x => equal(x.circuit, job?.circuit));
  require(scope && Number.isSafeInteger(job.sequence) && job.sequence >= 1, "Scheduled circuit does not match invitation");
  artifact(job.input, MAX_INPUT);
  const size = shapeSize(job.circuit.shape);
  require(job.input.size === size || job.input.size === size + 32, "Input size disagrees with circuit shape");
  if (job.sequence === 1) require(sameArtifact(job.input, job.circuit.initial), "Initial parameters differ from manifest");
  else require(job.input.size === size + 32, "Contribution challenge is missing");
  return job;
}
export class HTTPError extends Error {
  constructor(status, message) { super(message); this.status = status; }
  get retryable() { return this.status === 429 || this.status >= 500; }
}
export const retryable = e => e instanceof TypeError || e.name === "TimeoutError" || e instanceof HTTPError && e.retryable;
export function deadline(signal, ms = 30000) {
  return signal ? AbortSignal.any([signal, AbortSignal.timeout(ms)]) : AbortSignal.timeout(ms);
}
export async function readBounded(response, limit, expected) {
  require(response.body, "Response body is missing");
  const bytes = new Uint8Array(expected ?? limit);
  const reader = response.body.getReader();
  let n = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      require(value.length <= bytes.length - n, "Response exceeds its allowed size");
      bytes.set(value, n); n += value.length;
    }
    require(expected === undefined || n === expected, "Incomplete download");
    return n === bytes.length ? bytes : bytes.slice(0, n);
  } finally { await reader.cancel().catch(() => {}); }
}
export async function request(path, { token, method = "GET", body, signal, timeout = 30000 } = {}) {
  require(path.startsWith("/v1/"), "Invalid API path");
  const headers = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body) headers["Content-Type"] = "application/octet-stream";
  const response = await fetch(path, { method, body, headers, credentials: "omit", redirect: "error", cache: "no-store", signal: deadline(signal, timeout) });
  if (!response.ok) {
    const bytes = await readBounded(response, 4096).catch(() => new Uint8Array());
    const message = new TextDecoder().decode(bytes).trim();
    throw new HTTPError(response.status, message || `Coordinator returned HTTP ${response.status}`);
  }
  return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(await readBounded(response, MAX_JSON)));
}
const metadata = new Map();
let metadataBytes = 0;
export async function publicJSON(ref, signal) {
  artifact(ref, MAX_JSON);
  const cached = metadata.get(ref.sha256);
  if (cached && cached.size === ref.size) return cached.value;
  const bytes = await sprFetch(`/v1/objects/${ref.sha256}`, MAX_JSON, ref.size, undefined, deadline(signal));
  require(await sprSHA256(bytes) === ref.sha256, "Published metadata checksum mismatch");
  const value = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
  while (metadata.size && metadataBytes + ref.size > (32 << 20)) {
    const oldest = metadata.keys().next().value;
    metadataBytes -= metadata.get(oldest).size; metadata.delete(oldest);
  }
  metadata.set(ref.sha256, { size: ref.size, value }); metadataBytes += ref.size;
  return value;
}
export async function authenticate(invitation, pin, signal) {
  require(invitation?.format === FORMAT && invitation.mode === "queue" && invitation.track === "core", "A queued core invitation is required");
  require(hex(invitation.token) && hex(pin) && invitation.manifest_sha256 === pin && id(invitation.contributor), "Invitation identity or manifest pin is invalid");
  require(typeof invitation.insecure_test_setup === "boolean" && Date.parse(invitation.expires) > Date.now(), "Invitation expired; request renewal with the same pseudonym");
  const origin = new URL(invitation.url);
  require(origin.origin === location.origin && ["", "/"].includes(origin.pathname) && !origin.search && !origin.hash && !origin.username && !origin.password, "Invitation belongs to a different coordinator origin");
  require(Array.isArray(invitation.jobs) && invitation.jobs.length > 0 && invitation.jobs.length <= 41, "Invalid core circuit count");
  const bytes = await sprFetch("/v1/manifest", MAX_JSON, undefined, undefined, deadline(signal));
  require(await sprSHA256(bytes) === pin, "Coordinator manifest does not match the private invitation");
  const manifest = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
  require(manifest.format === FORMAT && manifest.engine === ENGINE && manifest.phase1?.insecure_test_setup === invitation.insecure_test_setup, "Manifest engine or rehearsal marker mismatch");
  const seen = new Set();
  for (const job of invitation.jobs) {
    const c = job.circuit;
    require(c?.spec?.track === "core" && id(c.spec.id) && !seen.has(c.spec.id), "Invalid or duplicate invitation circuit");
    require(manifest.circuits.some(x => equal(x, c)), "Invitation circuit differs from authenticated manifest");
    if (!invitation.insecure_test_setup) {
      const s = c.spec;
      require((s.family === "transfer-confidential" && s.id === `transfer_confidential_${s.inputs}_${s.outputs}`) || (s.family === "merge" && [8, 24, 52].includes(s.inputs) && s.outputs === 1 && s.id === `merge_${s.inputs}_1`), "Unsupported core circuit family");
    }
    checkJob(job, invitation); seen.add(c.spec.id);
  }
  return { manifest, ref: { sha256: pin, size: bytes.length } };
}

// Read a single immutable checkpoint and authenticate its state objects. A
// receipt's inclusion is separate from replaying the cryptographic transcript.
export async function snapshot(invitation, manifestRef, signal) {
  const head = await request("/v1/checkpoint", { signal });
  const cp = await publicJSON(head.ref, signal);
  require(cp.format === FORMAT && sameArtifact(cp.manifest, manifestRef) && cp.states && Number.isSafeInteger(cp.sequence), "Checkpoint manifest mismatch");
  const receipts = [], states = new Map();
  for (const scope of invitation.jobs) {
    const name = scope.circuit.spec.id;
    const state = await publicJSON(cp.states[name], signal);
    require(state.circuit === name && Array.isArray(state.history), "Checkpoint circuit mismatch");
    let parent = scope.circuit.initial;
    for (let n = 0; n < state.history.length; n++) {
      const r = state.history[n];
      require(r.circuit === name && r.sequence === n + 1 && sameArtifact(r.parent, parent) && id(r.contributor) && Number.isFinite(Date.parse(r.accepted_at)), "Broken receipt history");
      artifact(r.output);
      require(r.output.size === shapeSize(scope.circuit.shape) + 32, "Receipt output size mismatch");
      if (r.contributor === invitation.contributor) receipts.push(r);
      parent = r.output;
    }
    require(sameArtifact(parent, state.head), "Checkpoint head differs from receipt history");
    states.set(name, state);
  }
  return { ref: head.ref, checkpoint: cp, states, receipts };
}
export function checkReceipt(r, job, invitation, output) {
  require(r && r.circuit === job.circuit.spec.id && r.sequence === job.sequence && r.contributor === invitation.contributor && sameArtifact(r.parent, job.input) && sameArtifact(r.output, output) && Number.isFinite(Date.parse(r.accepted_at)), "Acceptance receipt does not match the local contribution");
}
export function sleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(signal.reason);
    const done = () => { signal?.removeEventListener("abort", abort); resolve(); };
    const timer = setTimeout(done, ms);
    const abort = () => { clearTimeout(timer); reject(signal.reason); };
    signal?.addEventListener("abort", abort, { once: true });
  });
}
