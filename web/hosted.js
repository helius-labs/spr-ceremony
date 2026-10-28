import { usesPublicPhase1, ENGINE, hex, require, request, deadline } from "./protocol.js";

const $ = id => document.getElementById(id);
let build, config, invitationLink, githubTicket, githubConfirmation;
function status(text, error = false) {
  $("status").hidden = false;
  $("status").textContent = text;
  $("status").classList.toggle("error", error);
}
function readInvitationLink() {
  const fragment = new URLSearchParams(location.hash.slice(1));
  history.replaceState(null, "", location.pathname);
  invitationLink = undefined;
  $("invitation-actions").hidden = true;
  $("invitation-help").textContent = "Get a private invitation from the operator. Paste it when the terminal asks.";
  if (!fragment.size) return;
  if (fragment.size === 1 && fragment.has("github")) {
    $("github-panel").hidden = false;
    $("native-panel").hidden = true;
    const value = fragment.get("github");
    if (hex(value)) { githubTicket = value; $("github-begin").hidden = false; }
    else if (value === "confirm") void showGitHubConfirmation();
    else $("github-status").textContent = "GitHub linking did not finish. Run spr-ceremony link-github to try again.";
    return;
  }
  const token = fragment.get("token"), pin = fragment.get("manifest");
  if (fragment.size === 2 && hex(token) && hex(pin)) {
    invitationLink = `${location.origin}/#${new URLSearchParams({ token, manifest: pin })}`;
    $("invitation-actions").hidden = false;
    $("invitation-help").textContent = "Run the command, then paste your private invitation when asked.";
    $("status").hidden = true;
  } else status("Invalid invitation link. Ask the operator for a new one.", true);
}
readInvitationLink();
window.addEventListener("hashchange", readInvitationLink);
function commands() {
  const name = `spr-ceremony-${$("platform").value}`;
  $("native-command").textContent = `chmod +x ${name}\n./${name} join`;
  const hash = config.native.find(x => x.name === name)?.sha256;
  const bundle = config.provenance === "/native/provenance.json";
  const transport = location.protocol === "https:" ? "--proto '=https' --tlsv1.2" : "--proto '=http'";
  $("verify-command").textContent = `${$("platform").value.startsWith("darwin") ? "shasum -a 256" : "sha256sum"} ${name}${hash ? `\n# Expected SHA256: ${hash}` : ""}${bundle ? `\ncurl ${transport} -fsS ${shellQuote(location.origin + config.provenance)} -o provenance.json` : ""}\ngh attestation verify ${name} --repo helius-labs/spr-ceremony ${bundle ? "--bundle provenance.json " : ""}--signer-workflow helius-labs/spr-ceremony/.github/workflows/binaries.yml --source-digest ${build.source_revision} --deny-self-hosted-runners`;
}
function shellQuote(value) { return "'" + value.replaceAll("'", "'\\''") + "'"; }
async function publicView() {
  const view = await request("/v1/ceremony");
  require(view.format === "spr-public-ceremony-v1" && hex(view.manifest?.sha256) && hex(view.checkpoint?.sha256) && Array.isArray(view.circuits) && view.circuits.length <= 41, "Invalid public ceremony metadata");
  $("public-summary").textContent = `${view.phase[0].toUpperCase() + view.phase.slice(1)} · ${view.circuits.length} circuits · ${view.participants} contributors`;
  $("keys-status").textContent = view.phase === "finalized" ? "Final keys. Verify before use." : "Final keys will appear after the ceremony is finalized.";
  if (view.insecure_test_setup && !usesPublicPhase1(view.phase1)) { $("notice").hidden = false; $("notice").textContent = "Locally generated parameters. Do not use these keys in production."; }
  function link(parent, label, ref) {
    require(hex(ref?.sha256) && Number.isSafeInteger(ref.size) && ref.size > 0, "Invalid public artifact");
    const a = document.createElement("a"); a.href = `/v1/objects/${ref.sha256}`; a.textContent = label; a.title = `SHA256: ${ref.sha256}`; a.download = ref.sha256; parent.append(a);
  }
  const parameters = $("parameter-links"); parameters.replaceChildren(); parameters.className = "key-row";
  link(parameters, "Phase 1", view.phase1); link(parameters, "Manifest", view.manifest); link(parameters, "Checkpoint", view.checkpoint);
  if (view.coordinator_key) link(parameters, "Signing key", view.coordinator_key);
  if (view.final_export) link(parameters, "Export manifest", view.final_export);
  $("public-pins").textContent = `Manifest: ${view.manifest.sha256}\nCheckpoint: ${view.checkpoint.sha256}`;
  $("keys").replaceChildren();
  for (const circuit of view.circuits) {
    const row = document.createElement("div"); row.className = "key-row";
    const label = document.createElement("p"); label.textContent = `${circuit.id} · ${circuit.contributions} contributions`; row.append(label);
    link(row, "Circuit", circuit.r1cs); link(row, "Phase 2", circuit.head);
    if (view.phase === "finalized") { link(row, "Proving key", circuit.pk); link(row, "Verifying key", circuit.vk); }
    $("keys").append(row);
  }
}
async function publicRecords() {
  try {
    const view = await request("/v1/records");
    require(hex(view.manifest?.sha256) && hex(view.checkpoint?.sha256) && Array.isArray(view.records), "Invalid contribution records");
    $("records").replaceChildren();
    for (const record of view.records) {
      require(hex(record.invitation_id) && typeof record.contributor === "string" && record.status === "accepted" && Number.isInteger(record.total) && record.total > 0 && record.total <= 41, "Invalid contribution record");
      const row = document.createElement("div"); row.className = "key-row";
      const link = document.createElement("a"); link.href = `/v1/records/${record.invitation_id}`;
      link.textContent = `${record.contributor} · ${record.total}/${record.total} accepted`;
      row.append(link); $("records").append(row);
      void showGitHubIdentity(row, record, view.manifest);
    }
    $("records-status").textContent = view.records.length ? "Download signed receipts:" : "Completed contributions appear here automatically.";
  } catch { $("records-status").textContent = "Contribution records are temporarily unavailable."; }
}
async function initialize() {
  build = JSON.parse(new TextDecoder().decode(await sprFetch("/build.json", 64 << 10, undefined, undefined, deadline())));
  config = JSON.parse(new TextDecoder().decode(await sprFetch("/ui-config.json", 64 << 10, undefined, undefined, deadline())));
  require(build.format === "spr-browser-build-v1" && build.engine === ENGINE && /^[a-f0-9]{40}$/.test(build.source_revision) && config.format === "spr-hosted-ui-v1" && config.origin === location.origin, "Hosted configuration or engine mismatch");
  $("build").textContent = `Engine ${build.engine} · ${build.go_version} · source ${build.source_revision}${build.source_modified ? " (modified build)" : ""}`;
  $("revision").textContent = `Expected source revision: ${build.source_revision}`;
  for (const item of config.native) {
    require(/^spr-ceremony-(darwin|linux)-(arm64|amd64)$/.test(item.name) && item.url === `/native/${item.name}` && hex(item.sha256), "Invalid native download metadata");
    const a = document.createElement("a"); a.className = "button secondary"; a.href = item.url; a.download = item.name;
    a.textContent = {"darwin-arm64": "Mac · Apple silicon", "darwin-amd64": "Mac · Intel", "linux-amd64": "Linux · x86-64", "linux-arm64": "Linux · ARM64"}[item.name.replace("spr-ceremony-", "")]; $("downloads").append(a);
  }
  $("native-unavailable").hidden = config.native.length !== 0;
  if (config.installer && location.protocol === "https:") {
    require(config.installer === "/join.sh" && /^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$/.test(config.version), "Invalid installer metadata");
    $("one-liner").hidden = false;
    $("join-command").textContent = `curl --proto '=https' --tlsv1.2 -fsS ${shellQuote(location.origin + "/join.sh")} | SPR_CEREMONY_DOWNLOAD_BASE=${shellQuote(location.origin + "/native")} bash`;
  }
  commands();
  await publicView();
}
$("copy-command").addEventListener("click", async () => {
  try { await navigator.clipboard.writeText($("join-command").textContent); status("Command copied."); }
  catch { status("Select and copy the command above."); }
});
$("copy-invitation").addEventListener("click", async () => {
  if (!invitationLink) return;
  try { await navigator.clipboard.writeText(invitationLink); status("Invitation copied. Paste it at the hidden terminal prompt."); }
  catch { status("Use your original private invitation link."); }
});
$("platform").addEventListener("change", () => { if (config && build) commands(); });
initialize().catch(() => status("Ceremony information is unavailable. Retry, or use your existing terminal client.", true));
void publicRecords();
// Invitation fragments never leave this page. Only the separate, short-lived
// GitHub flow uses a cookie; contributions still run exclusively in the CLI.

async function githubRequest(path, body) {
  const response = await fetch(path, {method: body === undefined ? "GET" : "POST", credentials: "same-origin", redirect: "error", cache: "no-store", headers: body === undefined ? {} : {"Content-Type": "application/json"}, body: body === undefined ? undefined : JSON.stringify(body), signal: deadline()});
  if (!response.ok) throw new Error("GitHub linking failed");
  const reader = response.body.getReader();
  const bytes = new Uint8Array(32 << 10); let length = 0;
  try {
    while (true) {
      const {value, done} = await reader.read(); if (done) break;
      require(length + value.length <= bytes.length, "Oversized GitHub response"); bytes.set(value, length); length += value.length;
    }
  } finally { await reader.cancel().catch(() => {}); }
  return JSON.parse(new TextDecoder().decode(bytes.subarray(0, length)));
}
async function showGitHubConfirmation() {
  try {
    const session = await githubRequest("/v1/github/session");
    require(Number.isSafeInteger(session.github_id) && session.github_id > 0 && typeof session.github_login === "string" && /^[a-zA-Z0-9-]{1,39}$/.test(session.github_login) && hex(session.confirm) && typeof session.contributor === "string", "Invalid GitHub session");
    githubConfirmation = session.confirm;
    $("github-status").textContent = `Link @${session.github_login} to ${session.contributor} · ${session.total}/${session.total} accepted? This will be public.`;
    $("github-confirm").hidden = false;
  } catch { $("github-status").textContent = "Link expired. Run spr-ceremony link-github to try again."; }
}
$("github-begin").addEventListener("click", async () => {
  $("github-begin").disabled = true;
  try {
    const result = await githubRequest("/v1/github/begin", {ticket: githubTicket});
    const target = new URL(result.url);
    require(target.origin === "https://github.com" && target.pathname === "/login/oauth/authorize" && !target.username && !target.password && !target.hash, "Invalid GitHub authorization URL");
    githubTicket = undefined;
    location.assign(target.href);
  } catch { $("github-status").textContent = "Link expired or unavailable. Run spr-ceremony link-github to try again."; }
});
$("github-confirm").addEventListener("click", async () => {
  $("github-confirm").disabled = true;
  try {
    const identity = await githubRequest("/v1/github/confirm", {confirm: githubConfirmation});
    require(identity.format === "spr-github-identity-v1" && hex(identity.invitation_id), "Invalid GitHub identity");
    githubConfirmation = undefined;
    $("github-confirm").parentElement.hidden = true;
    $("github-title").textContent = "GitHub linked.";
    $("github-status").textContent = `Linked @${identity.github_login}. Your public record is ready. GitHub publication follows automatically.`;
    const link = document.createElement("a"); link.href = `/v1/github/identities/${identity.invitation_id}`; link.textContent = "Download signed account link"; $("github-panel").append(link);
    await publicRecords();
  } catch { $("github-status").textContent = "Could not publish the link. Retry with spr-ceremony link-github, using the original GitHub account if already linked."; }
});
async function showGitHubIdentity(row, record, manifest) {
  try {
    const identity = await request(`/v1/github/identities/${record.invitation_id}`);
    require(identity.format === "spr-github-identity-v1" && identity.invitation_id === record.invitation_id && identity.manifest?.sha256 === manifest.sha256 && Number.isSafeInteger(identity.github_id) && identity.github_id > 0 && /^[a-zA-Z0-9-]{1,39}$/.test(identity.github_login), "Invalid GitHub identity");
    const profile = document.createElement("a"); profile.href = `https://github.com/${identity.github_login}`; profile.rel = "noopener noreferrer"; profile.textContent = `@${identity.github_login} · GitHub verified`; profile.title = `Account ID ${identity.github_id}; linked ${identity.linked_at}`;
    const proof = document.createElement("a"); proof.href = `/v1/github/identities/${record.invitation_id}`; proof.textContent = "Account proof";
    row.append(profile, proof);
  } catch { /* Account linking is optional; receipt visibility never depends on it. */ }
}
