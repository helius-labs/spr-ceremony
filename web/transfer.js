import { artifact, sameArtifact, require, readBounded, request, deadline, sleep, retryable, HTTPError, MAX_INPUT } from "./protocol.js";

const CHUNK = 4 << 20;
export async function download(ref, cache, config, signal, progress) {
  artifact(ref, MAX_INPUT);
  await cache.clearParts(ref.sha256);
  const output = new Uint8Array(ref.size);
  for (let start = 0; start < ref.size; start += CHUNK) {
    signal.throwIfAborted();
    const end = Math.min(start + CHUNK, ref.size) - 1;
    const key = `part:${ref.sha256}:${start}`;
    let part = await cache.get(key);
    if (!(part instanceof ArrayBuffer) || part.byteLength !== end - start + 1) {
      part = null;
      for (let attempt = 0; attempt < 4; attempt++) {
        try {
          let url = `/v1/objects/${ref.sha256}`;
          if (config.download_origin) {
            const ticket = await request(`/v1/download/${ref.sha256}`, { signal });
            require(sameArtifact(ticket.artifact, ref), "Download ticket artifact mismatch");
            const target = new URL(ticket.url, location.origin);
            require(target.origin === config.download_origin && !target.username && !target.password && !target.hash && (target.protocol === "https:" || target.origin === location.origin || config.rehearsal), "Download ticket uses an unapproved origin");
            url = target.href;
          }
          const response = await fetch(url, { headers: { Range: `bytes=${start}-${end}` }, mode: "cors", credentials: "omit", redirect: "error", cache: "no-store", signal: deadline(signal, 60000) });
          if (response.status !== 200 && response.status !== 206) {
            await response.body?.cancel();
            // A fresh ticket is obtained on every retry, including an expired signature.
            throw new HTTPError(response.status === 403 ? 503 : response.status, `Parameter download failed (HTTP ${response.status})`);
          }
          if (response.status === 200) {
            // Range-ignorant proxies may return the whole authenticated object.
            const all = await readBounded(response, MAX_INPUT, ref.size);
            require(await sprSHA256(all) === ref.sha256, "Parameter checksum mismatch");
            return all;
          }
          if (response.headers.get("Content-Range") !== `bytes ${start}-${end}/${ref.size}`) {
            await response.body?.cancel();
            throw new Error("Invalid download range");
          }
          part = (await readBounded(response, CHUNK, end - start + 1)).buffer;
          await cache.put(key, part);
          break;
        } catch (e) {
          if (signal.aborted || !retryable(e) || attempt === 3) throw e;
          await sleep(1000 * 2 ** attempt, signal);
        }
      }
    }
    output.set(new Uint8Array(part), start);
    progress(end + 1, ref.size);
  }
  if (await sprSHA256(output) !== ref.sha256) {
    await cache.clearParts();
    throw new Error("Parameter checksum mismatch. Cached transfer cleared; retry to download again.");
  }
  return output;
}
