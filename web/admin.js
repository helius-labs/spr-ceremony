"use strict";
const $ = id => document.getElementById(id);
let secret, clearTimer, view, busy = false, revoking;
function say(message, error = false) { $("status").textContent = message; $("status").classList.toggle("error", error); }
function clearSecret() {
  secret = null; $("link").value = ""; clearTimeout(clearTimer);
  if ($("secret").open) $("secret").close();
  $("secret").hidden = true;
}
function canInvite() { return view?.phase === "open" && (!view.beacon || Date.now() < Date.parse(view.beacon.round_time)); }
function existingInvitation() {
  const name = $("name").value.trim();
  return view?.invitations.find(i => i.contributor === name && i.has_link && Date.parse(i.expires) > Date.now());
}
function controls() {
  const existing = existingInvitation();
  $("name").disabled = $("hours").disabled = $("refresh").disabled = busy;
  $("invite").disabled = busy || !canInvite() || !!existing;
  $("existing").hidden = !existing;
  $("existing-name").textContent = existing ? `${existing.contributor} already has an invitation · ${existing.accepted}/${existing.total} accepted.` : "";
  $("resume-existing").disabled = busy || !canInvite();
  for (const button of document.querySelectorAll("#invitations button")) button.disabled = busy || (button.dataset.needsOpen === "true" && !canInvite());
}
async function api(path, data) {
  const response = await fetch(`/admin/${path}`, { method: data ? "POST" : "GET", cache: "no-store", credentials: "omit", redirect: "error", headers: { "X-SPR-Admin": "1", ...(data ? { "Content-Type": "application/json" } : {}) }, body: data ? JSON.stringify(data) : undefined, signal: AbortSignal.timeout(30000) });
  if (!response.ok) {
    // The admin API returns fixed operator-facing messages, never key material.
    const error = new Error((await response.text()).trim().slice(0, 200));
    error.adminMessage = true; throw error;
  }
  return response.json();
}
async function refresh() {
  view = await api("status");
  $("phase").textContent = view.phase[0].toUpperCase() + view.phase.slice(1);
  $("test").hidden = !view.insecure_test_setup;
  $("storage").textContent = view.s3 ? "S3 archive connected." : "Local storage. Connect S3 before launch.";
  $("beacon").textContent = view.beacon ? `Close before ${new Date(view.beacon.round_time).toLocaleString()} · beacon round ${view.beacon.round}` : "No beacon planned.";
  $("leases").textContent = `${view.reservations.length} active reservations`;
  $("pins").textContent = `Manifest: ${view.manifest.sha256}\nCheckpoint: ${view.checkpoint.sha256}${view.coordinator_key ? `\nSigning key: ${view.coordinator_key.sha256}` : ""}`;
  renderInvitations(); controls();
}
function renderInvitations() {
  $("invitations").replaceChildren();
  for (const invitation of view.invitations) {
    const row = document.createElement("div"); row.className = "invitation-row"; row.dataset.id = invitation.id;
    const heading = document.createElement("div"); heading.className = "heading";
    const text = document.createElement("p"); text.textContent = `${invitation.contributor} · ${invitation.accepted}/${invitation.total} accepted`;
    const state = document.createElement("span"); state.className = "invitation-state";
    state.textContent = !invitation.has_link ? "Revoked" : Date.parse(invitation.expires) <= Date.now() ? "Expired" : "Active";
    heading.append(text, state);
    const expiry = document.createElement("p"); expiry.className = "detail";
    expiry.textContent = !invitation.has_link ? "Access disabled. Accepted contributions are kept." : `Expires ${new Date(invitation.expires).toLocaleString()}`;
    const identity = document.createElement("p"); identity.className = "detail"; identity.textContent = `ID ${invitation.id.slice(0, 12)}`; identity.title = invitation.id;
    const buttons = document.createElement("div"); buttons.className = "buttons invitation-actions";
    const reissue = document.createElement("button"); reissue.className = "secondary"; reissue.textContent = "Reissue link"; reissue.dataset.needsOpen = "true";
    reissue.addEventListener("click", () => reissueLink(invitation.id));
    buttons.append(reissue);
    if (invitation.has_link) {
      const revoke = document.createElement("button"); revoke.className = "secondary"; revoke.textContent = "Revoke";
      revoke.addEventListener("click", () => { revoking = invitation.id; renderInvitations(); controls(); });
      buttons.append(revoke);
    }
    row.append(heading, expiry, identity, buttons);
    if (revoking === invitation.id && invitation.has_link) {
      const confirmation = document.createElement("div"); confirmation.className = "revoke-confirmation";
      const explanation = document.createElement("p"); explanation.textContent = `Revoke ${invitation.contributor}’s access? Accepted contributions stay.`;
      const confirm = document.createElement("button"); confirm.textContent = "Revoke access";
      confirm.addEventListener("click", () => action(async () => {
        await api("revoke", { id: invitation.id }); revoking = null; clearSecret(); await refresh(); say("Access revoked.");
      }));
      const cancel = document.createElement("button"); cancel.className = "secondary"; cancel.textContent = "Cancel";
      cancel.addEventListener("click", () => { revoking = null; renderInvitations(); controls(); });
      const actions = document.createElement("div"); actions.className = "buttons"; actions.append(confirm, cancel);
      confirmation.append(explanation, actions); row.append(confirmation);
    }
    $("invitations").append(row);
  }
  if (!view.invitations.length) $("invitations").textContent = "No invitations yet.";
}
function showSecret(value, reissued = false) {
  clearSecret(); secret = value;
  $("secret-name").textContent = value.invitation.contributor;
  $("secret-context").textContent = reissued ? "New link. Previous link revoked. Progress preserved." : "Share this invitation privately.";
  $("secret-expiry").textContent = `Expires ${new Date(value.invitation.expires).toLocaleString()}`;
  $("link").value = secret.link; $("secret").hidden = false; $("secret").showModal();
  clearTimer = setTimeout(clearSecret, 300000);
}
function reissueLink(id) {
  return action(async () => {
    clearSecret();
    const created = await api("reissue", { id, hours: 24 });
    revoking = null; await refresh(); showSecret(created, true); say("Previous link replaced. Progress preserved.");
  });
}
async function action(fn) {
  if (busy) return;
  busy = true; controls();
  try { await fn(); }
  catch (error) { say(error.adminMessage ? error.message : "Connection interrupted. Refresh and try again.", true); }
  finally { busy = false; controls(); }
}
$("invite").addEventListener("click", () => action(async () => {
  const name = $("name").value.trim();
  if (!/^[a-z0-9][a-z0-9_-]{0,99}$/.test(name)) { say("Use lowercase letters, numbers, underscores or hyphens.", true); return; }
  clearSecret();
  const created = await api("invite", { name, hours: Number($("hours").value) });
  $("name").value = ""; await refresh(); showSecret(created); say("Share the private link.");
}));
$("name").addEventListener("input", () => { clearSecret(); controls(); });
$("resume-existing").addEventListener("click", () => { const existing = existingInvitation(); if (existing) reissueLink(existing.id); });
$("copy").addEventListener("click", () => action(async () => { if (secret) { await navigator.clipboard.writeText(secret.link); $("secret-context").textContent = "Copied. Share privately."; } }));
$("download").addEventListener("click", () => { if (!secret) return; const url = URL.createObjectURL(new Blob([JSON.stringify(secret.invitation, null, 2) + "\n"], { type: "application/json" })); const link = document.createElement("a"); link.href = url; link.download = "invitation.json"; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); });
$("clear").addEventListener("click", () => { clearSecret(); say("Private link cleared."); });
$("secret").addEventListener("close", clearSecret);
$("refresh").addEventListener("click", () => action(refresh));
window.addEventListener("pagehide", clearSecret);
action(async () => { await refresh(); say("Ready."); });
