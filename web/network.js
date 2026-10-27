"use strict";

// Bounded streaming avoids buffering an untrusted oversized response first.
globalThis.sprFetch = async function (path, limit, expected, progress, signal) {
  const response = await fetch(path, { credentials: "omit", redirect: "error", cache: "no-store", signal });
  if (!response.ok || !response.body) throw new Error(`Download failed: HTTP ${response.status}`);
  if (!Number.isSafeInteger(limit) || limit <= 0 || (expected !== undefined && (!Number.isSafeInteger(expected) || expected <= 0 || expected > limit))) {
    throw new Error("Invalid download limit");
  }
  const bytes = new Uint8Array(expected ?? limit);
  const reader = response.body.getReader();
  let offset = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      if (value.length > bytes.length - offset) throw new Error("Download exceeds expected size");
      bytes.set(value, offset);
      offset += value.length;
      if (progress) progress(offset);
    }
    if (expected !== undefined && offset !== expected) throw new Error("Incomplete download");
    return offset === bytes.length ? bytes : bytes.slice(0, offset);
  } finally {
    await reader.cancel().catch(() => {});
  }
};

globalThis.sprSHA256 = async function (bytes) {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(digest, x => x.toString(16).padStart(2, "0")).join("");
};
