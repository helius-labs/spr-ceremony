"use strict";
const $ = id => document.getElementById(id);
let secret, clearTimer;
function say(message, error = false) { $("status").textContent = message; $("status").classList.toggle("error", error); }
function clearSecret() { secret = null; $("link").value = ""; $("secret").hidden = true; clearTimeout(clearTimer); }
async function api(path, data) {
  const response = await fetch(`/admin/${path}`, { method: data ? "POST" : "GET", cache: "no-store", credentials: "omit", redirect: "error", headers: { "X-SPR-Admin": "1", ...(data ? { "Content-Type": "application/json" } : {}) }, body: data ? JSON.stringify(data) : undefined, signal: AbortSignal.timeout(30000) });
  if (!response.ok) throw new Error((await response.text()).slice(0, 200));
  return response.json();
}
async function refresh() {
  const view = await api("status");
  $("phase").textContent = view.phase[0].toUpperCase() + view.phase.slice(1);
  $("test").hidden = !view.insecure_test_setup;
  $("storage").textContent = view.s3 ? "S3 archive connected." : "Local storage. Connect S3 before launch.";
  $("beacon").textContent = view.beacon ? `Close before ${new Date(view.beacon.round_time).toLocaleString()} · beacon round ${view.beacon.round}` : "No beacon planned.";
  $("leases").textContent = `${view.reservations.length} active reservations`;
  $("pins").textContent = `Manifest: ${view.manifest.sha256}\nCheckpoint: ${view.checkpoint.sha256}`;
  $("invite").disabled = view.phase !== "open" || (view.beacon && Date.now() >= Date.parse(view.beacon.round_time));
  $("invitations").replaceChildren();
  for (const invitation of view.invitations) {
    const row = document.createElement("div"); row.className = "invitation-row";
    const text = document.createElement("p"); text.textContent = `${invitation.contributor} · ${invitation.accepted}/${invitation.total} accepted`;
    const expiry = document.createElement("p"); expiry.className = "detail"; expiry.textContent = `${Date.parse(invitation.expires) < Date.now() ? "Expired" : "Expires"} ${new Date(invitation.expires).toLocaleString()}`;
    const revoke = document.createElement("button"); revoke.className = "secondary"; revoke.textContent = "Revoke";
    revoke.addEventListener("click", () => action(async () => { if (!confirm(`Revoke access for ${invitation.contributor}? Accepted contributions remain.`)) return; await api("revoke", { id: invitation.id }); clearSecret(); await refresh(); say("Access revoked."); }));
    row.append(text, expiry, revoke); $("invitations").append(row);
  }
  if (!view.invitations.length) $("invitations").textContent = "No invitations yet.";
}
async function action(fn) { try { await fn(); } catch { say("Request failed. Refresh and check the ceremony state.", true); } }
$("invite").addEventListener("click", () => action(async () => {
  const name = $("name").value.trim(); if (!/^[a-z0-9][a-z0-9_-]{0,99}$/.test(name)) { say("Use lowercase letters, numbers, underscores or hyphens.", true); return; }
  $("invite").disabled = true; clearSecret();
  try { secret = await api("invite", { name, hours: Number($("hours").value) }); $("link").value = secret.link; $("secret").hidden = false; clearTimer = setTimeout(clearSecret, 300000); say("Share the private link."); }
  finally { await refresh(); }
}));
$("copy").addEventListener("click", () => action(async () => { if (secret) { await navigator.clipboard.writeText(secret.link); say("Copied. Share privately."); } }));
$("download").addEventListener("click", () => { if (!secret) return; const url = URL.createObjectURL(new Blob([JSON.stringify(secret.invitation, null, 2) + "\n"], { type: "application/json" })); const link = document.createElement("a"); link.href = url; link.download = "invitation.json"; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); });
$("clear").addEventListener("click", () => { clearSecret(); say("Private link cleared."); });
$("refresh").addEventListener("click", () => action(refresh));
window.addEventListener("pagehide", clearSecret);
action(async () => { await refresh(); say("Ready."); });
