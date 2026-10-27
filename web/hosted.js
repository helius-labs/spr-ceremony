import { ENGINE, hex, require, authenticate, request, deadline, MAX_JSON } from "./protocol.js";
import { PublicCache } from "./cache.js";
import { Session } from "./session.js";

const $ = id => document.getElementById(id);
let invitation, manifestRef, build, config, cache, session, active = false, refreshing = false, wakeLock;
let ready;
const browserCapable = typeof Worker === "function" && typeof WebAssembly === "object" && !!navigator.locks;
const fragment = new URLSearchParams(location.hash.slice(1));
history.replaceState(null, "", location.pathname);
const labels = {
  queue: "Joining…", waiting: "Waiting for a circuit…",
  download: "Downloading…", load_engine: "Loading…",
  decode: "Checking parameters…", contribute: "Contributing…",
  verify: "Verifying locally…", encode: "Preparing upload…", computed: "Saving…",
  upload: "Uploading. Awaiting verification…", receipt: "Checking receipt…",
  accepted: "Accepted.", complete: "Complete. Save your receipts."
};
function formatBytes(n) { return n >= 1e6 ? `${(n / 1e6).toFixed(n >= 100e6 ? 0 : 1)} MB` : `${(n / 1e3).toFixed(1)} KB`; }
function status(text, error = false) { $("status").textContent = text; $("status").classList.toggle("error", error); }
function update(event) {
  if (event.stage === "progress") {
    $("count").textContent = `${event.completed} / ${event.total} accepted`;
    $("receipts").disabled = !event.receipts.length;
    return;
  }
  if (event.stage === "circuit") {
    $("circuit").textContent = `${event.circuit} · #${event.sequence} · ${formatBytes(event.bytes)}`;
  }
  if (event.stage === "download" && event.total) $("progress").value = 100 * event.done / event.total;
  else if (event.stage === "complete") $("progress").value = 100;
  else $("progress").removeAttribute("value");
  if (event.message || labels[event.stage]) status(event.message || labels[event.stage]);
  // Public diagnostics for support/automation, never invitations or signed URLs.
  document.documentElement.dataset.stage = event.stage;
}
function save(name, data, type = "application/json") {
  const blob = data instanceof Blob ? data : new Blob([JSON.stringify(data, null, 2) + "\n"], { type });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a"); link.href = url; link.download = name;
  document.body.append(link); link.click(); link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60000);
}
function newSession() { return new Session(invitation, manifestRef, build, { ...config, rehearsal: invitation.insecure_test_setup }, cache, update); }
async function connect(value, pin) {
  require(!invitation, "Reload this page to switch invitations");
  status("Checking your invitation…");
  const authenticated = await authenticate(value, pin);
  const opened = await PublicCache.open(`${value.manifest_sha256}:${value.contributor}`);
  invitation = value; manifestRef = authenticated.ref; cache = opened;
  session = newSession();
  $("join").hidden = true; $("participant").hidden = false;
  $("participant-title").textContent = invitation.contributor;
  const bytes = invitation.jobs.reduce((sum, j) => sum + j.input.size, 0);
  $("scope").textContent = `${invitation.jobs.length} circuit${invitation.jobs.length === 1 ? "" : "s"} · ${formatBytes(bytes)} each way`;
  $("manifest").textContent = `Manifest SHA256: ${manifestRef.sha256}`;
  if (invitation.insecure_test_setup) { $("notice").hidden = false; $("notice").textContent = "Test ceremony. These keys are not for production."; }
  $("start").disabled = true;
  await refresh();
}
async function refresh() {
  if (!invitation || active || refreshing) return;
  refreshing = true; $("refresh").disabled = true; $("start").disabled = true;
  try {
    session = newSession();
    const view = await session.inspect();
    const done = new Set(view.receipts.map(r => r.circuit)).size === invitation.jobs.length;
    if (done) update({ stage: "complete" });
    else status(view.checkpoint.lifecycle.phase === "open" ? "Ready when you are." : `Ceremony ${view.checkpoint.lifecycle.phase}. Your receipts are available.`);
    $("start").disabled = done || view.checkpoint.lifecycle.phase !== "open" || !browserCapable;
    if (!browserCapable) status("Use the terminal client in this browser.");
  } catch (e) { status(e.message, true); }
  finally { refreshing = false; $("refresh").disabled = false; }
}
function commands() {
  const name = `spr-ceremony-${$("platform").value}`;
  $("native-command").textContent = `chmod +x ${name}\n./${name} contribute --invitation invitation.json`;
  const hash = config.native.find(x => x.name === name)?.sha256;
  $("verify-command").textContent = `${$("platform").value.startsWith("darwin") ? "shasum -a 256" : "sha256sum"} ${name}${hash ? `\n# Expected SHA256: ${hash}` : ""}\ngh attestation verify ${name} --repo helius-labs/spr-ceremony --signer-workflow helius-labs/spr-ceremony/.github/workflows/binaries.yml --source-digest ${build.source_revision}`;
}
async function initialize() {
  require(isSecureContext && crypto.subtle && typeof AbortSignal.any === "function" && typeof indexedDB === "object", "Use a current desktop browser over HTTPS, or use the native client");
  build = JSON.parse(new TextDecoder().decode(await sprFetch("/build.json", 64 << 10, undefined, undefined, deadline())));
  config = JSON.parse(new TextDecoder().decode(await sprFetch("/ui-config.json", 64 << 10, undefined, undefined, deadline())));
  require(build.format === "spr-browser-build-v1" && build.engine === ENGINE && /^[a-f0-9]{40}$/.test(build.source_revision) && config.format === "spr-hosted-ui-v1" && config.origin === location.origin, "Hosted configuration or engine mismatch");
  $("build").textContent = `Engine ${build.engine} · ${build.go_version} · source ${build.source_revision}${build.source_modified ? " (modified rehearsal build)" : ""}`;
  $("revision").textContent = `Expected source revision: ${build.source_revision}`;
  for (const item of config.native) {
    require(/^spr-ceremony-(darwin|linux)-(arm64|amd64)$/.test(item.name) && item.url === `/native/${item.name}` && hex(item.sha256), "Invalid native download metadata");
    const a = document.createElement("a"); a.className = "button secondary"; a.href = item.url; a.download = item.name;
    a.textContent = {"darwin-arm64": "Mac · Apple silicon", "darwin-amd64": "Mac · Intel", "linux-amd64": "Linux · x86-64", "linux-arm64": "Linux · ARM64"}[item.name.replace("spr-ceremony-", "")]; $("downloads").append(a);
  }
  $("native-unavailable").hidden = config.native.length !== 0;
  commands();
  status("Your invitation stays private.");
}
$("invitation").addEventListener("change", async () => {
  try {
    await ready;
    const file = $("invitation").files[0]; if (!file) return;
    require(file.size > 0 && file.size <= MAX_JSON, "Invitation file exceeds its size limit");
    const value = JSON.parse(await file.text()); await connect(value, value.manifest_sha256);
  } catch (e) { status(e.message, true); }
  finally { $("invitation").value = ""; }
});
$("start").addEventListener("click", async () => {
  if (active || refreshing || !invitation) return;
  active = true; session = newSession();
  document.documentElement.dataset.stage = "starting"; status("Resuming…");
  $("start").disabled = true; $("pause").disabled = false; $("refresh").disabled = true; $("native").disabled = true;
  const started = Date.now();
  const timer = setInterval(() => { $("elapsed").textContent = `${Math.floor((Date.now() - started) / 60000)} min ${Math.floor((Date.now() - started) / 1000) % 60} sec elapsed`; }, 1000);
  try {
    navigator.storage?.persist?.().catch(() => {});
    wakeLock = await navigator.wakeLock?.request("screen").catch(() => null);
    await session.run();
  } catch (e) {
    document.documentElement.dataset.stage = e.name === "AbortError" ? "paused" : "error";
    status(e.name === "AbortError" ? "Paused. Resume when ready." : `${e.message.replace(/[. ]+$/, "")}. Retry or use terminal.`, e.name !== "AbortError");
  } finally {
    active = false; clearInterval(timer); await wakeLock?.release().catch(() => {}); wakeLock = null;
    const complete = new Set(session.view?.receipts.map(r => r.circuit)).size === invitation.jobs.length;
    $("start").disabled = complete || !browserCapable; $("start").textContent = complete ? "Completed" : "Resume"; $("pause").disabled = true; $("refresh").disabled = false; $("native").disabled = false;
  }
});
$("pause").addEventListener("click", () => { $("pause").disabled = true; status("Pausing…"); session?.pause(); });
$("native").addEventListener("click", () => { $("native-panel").hidden = !$("native-panel").hidden; });
$("save-invitation").addEventListener("click", () => save("invitation.json", invitation));
$("receipts").addEventListener("click", () => { if (session.view?.receipts.length) save("receipts.json", session.attestation()); });
$("platform").addEventListener("change", commands);
$("refresh").addEventListener("click", refresh);
window.addEventListener("beforeunload", e => { if (active) { e.preventDefault(); e.returnValue = ""; } });
// No service worker, analytics, external scripts, cookies, or stored credentials.
ready = initialize();
ready.then(async () => {
  if (!fragment.size) return;
  const token = fragment.get("token"), pin = fragment.get("manifest");
  fragment.delete("token"); fragment.delete("manifest");
  require(hex(token) && hex(pin), "Invalid invitation link");
  const value = await request("/v1/invitation", { token });
  require(value.token === token, "Invitation token mismatch");
  await connect(value, pin);
}).catch(e => status(e.message, true));
setInterval(() => { if (invitation && !active && !document.hidden && document.documentElement.dataset.stage !== "error") refresh(); }, 15000);
