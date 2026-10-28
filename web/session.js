import { FORMAT, ENGINE, require, equal, sameArtifact, checkJob, checkReceipt, request, snapshot, sleep, retryable, MAX_INPUT } from "./protocol.js";
import { download } from "./transfer.js";

export class Session {
  constructor(invitation, manifestRef, build, config, cache, update) {
    Object.assign(this, { invitation, manifestRef, build, config, cache, update });
    this.controller = new AbortController();
  }
  event(stage, detail = {}) { this.update({ stage, ...detail }); }
  async inspect(signal = this.controller.signal) {
    const view = await snapshot(this.invitation, this.manifestRef, signal);
    const previous = await this.cache.get("checkpoint");
    if (previous) {
      require(view.checkpoint.sequence >= previous.sequence, "Coordinator checkpoint went backwards");
      if (view.checkpoint.sequence === previous.sequence) require(sameArtifact(view.ref, previous.ref), "Coordinator checkpoint changed at the same sequence");
    }
    // Any receipt we previously accepted must remain present in later snapshots.
    const saved = await this.cache.get("receipts") || [];
    require(saved.every(r => view.receipts.some(x => equal(x, r))), "A previously saved receipt is missing from the published checkpoint");
    await this.cache.put("receipts", view.receipts);
    await this.cache.put("checkpoint", { ref: view.ref, sequence: view.checkpoint.sequence });
    const pending = await this.cache.get("pending");
    if (pending) {
      const accepted = view.receipts.find(r => r.circuit === pending.job.circuit.spec.id);
      if (accepted) {
        if (sameArtifact(accepted.parent, pending.job.input) && sameArtifact(accepted.output, pending.report.output)) {
          await checkReceipt(accepted, pending.job, this.invitation, pending.report.output);
        } else {
          // Switching to native may accept a different output for the same
          // invitation. Count its public receipt without claiming this browser
          // computed that output; the unaccepted browser result is obsolete.
          this.event("elsewhere", { message: "A different output was accepted for this invitation. Discarding the unaccepted saved browser output." });
        }
        await this.cache.delete("pending");
        await this.cache.clearParts();
      }
    }
    this.view = view;
    this.event("progress", { completed: new Set(view.receipts.map(r => r.circuit)).size, total: this.invitation.jobs.length, receipts: view.receipts, phase: view.checkpoint.lifecycle.phase });
    return view;
  }
  async run() {
    const signal = this.controller.signal;
    require(navigator.locks, "This browser lacks cross-tab locking; use the native client");
    return navigator.locks.request(`spr:${this.invitation.manifest_sha256}:${this.invitation.id}`, { mode: "exclusive", ifAvailable: true }, async lock => {
      require(lock, "This invitation is already running in another tab. Pause that tab first.");
      let recoveries = 0;
      while (!signal.aborted) {
        const view = await this.inspect();
        if (new Set(view.receipts.map(r => r.circuit)).size === this.invitation.jobs.length) {
          this.event("complete", { receipts: view.receipts });
          return;
        }
        require(Date.parse(this.invitation.expires) > Date.now(), "Invitation expired; request a reissued link for this invitation");
        this.event("queue");
        const claim = await this.api("/v1/claim");
        if (claim.done) {
          const final = await this.inspect();
          require(new Set(final.receipts.map(r => r.circuit)).size === this.invitation.jobs.length, "Completion response is missing published receipts");
          this.event("complete", { receipts: final.receipts }); return;
        }
        if (!claim.job) {
          require(Number.isInteger(claim.wait_seconds) && claim.wait_seconds >= 1 && claim.wait_seconds <= 60, "Invalid queue wait");
          this.event("waiting");
          await sleep(claim.wait_seconds * 1000, signal); continue;
        }
        const job = checkJob(claim.job, this.invitation);
        this.job = job;
        try {
          const before = await snapshot(this.invitation, this.manifestRef, signal);
          const state = before.states.get(job.circuit.spec.id);
          require(sameArtifact(state.head, job.input) && state.history.length + 1 === job.sequence, "Reservation does not match the published circuit head");
          await this.runJob(job, claim);
          recoveries = 0;
        } catch (error) {
          if (!signal.aborted && (error.leaseLost || error.status === 409) && recoveries++ < 3) {
            this.event("reclaim", { message: "Reservation changed. Checking accepted work and rejoining the queue…" });
            await sleep(1000, signal);
            continue;
          }
          throw error;
        } finally {
          // Releasing an already accepted lease is harmless. Await release before
          // dropping the cross-tab lock so a new run cannot lose its reservation.
          await request(`/v1/release/${job.circuit.spec.id}`, { method: "POST", token: this.invitation.token, timeout: 5000 }).catch(() => {});
          this.job = null;
        }
      }
      signal.throwIfAborted();
    });
  }
  async api(path, options = {}) {
    for (let n = 0; ; n++) {
      try { return await request(path, { token: this.invitation.token, method: "POST", signal: this.controller.signal, ...options }); }
      catch (e) {
        if (!retryable(e) || n === 3 || this.controller.signal.aborted || options.signal?.aborted) throw e;
        this.event("retry", { message: "Connection interrupted. Retrying; completed public work is saved…" });
        await sleep(1000 * 2 ** n, options.signal || this.controller.signal);
      }
    }
  }
  async runJob(job, claim) {
    const control = new AbortController();
    const signal = AbortSignal.any([control.signal, this.controller.signal]);
    let expires = Date.parse(claim.expires), heartbeatError;
    require(expires > Date.now(), "Reservation already expired; check your device clock");
    const renew = async () => {
      const next = await request(`/v1/heartbeat/${job.circuit.spec.id}`, { token: this.invitation.token, method: "POST", signal, timeout: 20000 });
      if (next.done) return true;
      require(equal(checkJob(next.job, this.invitation), job) && Date.parse(next.expires) > Date.now(), "Reservation changed during contribution");
      expires = Date.parse(next.expires);
      return false;
    };
    const heartbeat = (async () => {
      while (!signal.aborted) {
        await sleep(Math.max(250, Math.min(30000, (expires - Date.now()) / 3)), signal);
        try { if (await renew()) return; }
        catch (e) {
          if (signal.aborted) return;
          if (retryable(e) && Date.now() < expires) continue;
          e.leaseLost = true; heartbeatError = e; control.abort(e); return;
        }
      }
    })().catch(e => { if (!signal.aborted) { heartbeatError = e; control.abort(e); } });
    try {
      this.event("circuit", { circuit: job.circuit.spec.id, sequence: job.sequence, bytes: job.input.size });
      let pending = await this.cache.get("pending");
      if (pending && !equal(pending.job, job)) {
        // A result on an old head cannot be transplanted onto a new head.
        await this.cache.delete("pending"); pending = null;
      }
      if (pending) {
        this.event("recover", { message: "Checking the saved public output…" });
        require(pending.output instanceof ArrayBuffer && pending.output.byteLength <= MAX_INPUT + 32, "Saved output has invalid size");
        require(pending.report.local_verification_passed && pending.output.byteLength === pending.report.output.size && await sprSHA256(pending.output) === pending.report.output.sha256, "Saved output checksum mismatch; clear this browser's site data or use the native client");
      } else {
        const estimate = await navigator.storage?.estimate?.();
        if (estimate?.quota) require(estimate.quota - estimate.usage >= 3 * job.input.size + (16 << 20), "Insufficient browser storage. Free storage or use the native client.");
        this.event("download");
        const input = await download(job.input, this.cache, this.config, signal, (done, total) => this.event("download", { done, total }));
        const fixtureText = JSON.stringify({ format: "spr-browser-contribution-v1", engine: ENGINE, track: "core", circuit: job.circuit.spec.id, manifest: this.manifestRef, input: job.input, shape: job.circuit.shape, insecure_test_setup: this.invitation.insecure_test_setup });
        const result = await this.compute(fixtureText, input, signal);
        require(result.report.local_verification_passed && sameArtifact(result.report.input, job.input) && result.report.circuit === job.circuit.spec.id && result.report.output.size === result.output.byteLength, "Contribution engine returned an inconsistent result");
        pending = { job, report: result.report, output: result.output };
        require(await sprSHA256(pending.output) === pending.report.output.sha256, "Output checksum mismatch");
        // Commit public output before upload: a reload or lost receipt reuses it.
        await this.cache.put("pending", pending);
      }
      signal.throwIfAborted();
      // Recheck after computation/tab suspension before spending time uploading.
      await renew();
      this.event("upload");
      const receipt = await this.api(`/v1/contributions/${job.circuit.spec.id}`, { body: new Blob([pending.output], { type: "application/octet-stream" }), signal, timeout: 30 * 60 * 1000 });
      await checkReceipt(receipt, job, this.invitation, pending.report.output);
      this.event("receipt");
      const view = await this.inspect(signal);
      require(view.receipts.some(r => equal(r, receipt)), "Acceptance receipt is absent from the published checkpoint");
      this.event("accepted", { receipt });
    } catch (e) { throw heartbeatError || e; }
    finally { control.abort(); await heartbeat; }
  }
  compute(fixtureText, input, signal) {
    return new Promise((resolve, reject) => {
      const worker = new Worker("worker.js");
      let settled = false;
      const finish = (error, result) => {
        if (settled) return;
        settled = true; worker.terminate(); signal.removeEventListener("abort", abort);
        if (error) reject(error); else resolve(result);
      };
      const abort = () => finish(signal.reason);
      if (signal.aborted) return abort();
      signal.addEventListener("abort", abort, { once: true });
      worker.onerror = () => finish(new Error("Browser worker stopped. Resume or use the native client if memory is limited."));
      worker.onmessage = ({ data }) => {
        if (data.type === "stage") this.event(data.stage === "complete" ? "computed" : data.stage, { memory: data.wasm_memory_bytes });
        if (data.type === "error") finish(new Error(data.error));
        if (data.type === "complete") finish(null, data);
      };
      worker.postMessage({ fixtureText, input: input.buffer, build: this.build }, [input.buffer]);
    });
  }
  pause() { this.controller.abort(new DOMException("Paused", "AbortError")); }
  attestation() {
    return { format: FORMAT, engine: ENGINE, manifest_sha256: this.invitation.manifest_sha256, invitation_id: this.invitation.id, contributor: this.invitation.contributor, track: "core", insecure_test_setup: this.invitation.insecure_test_setup, receipts: this.view?.receipts || [] };
  }
}
