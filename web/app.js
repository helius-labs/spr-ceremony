"use strict";

const $ = id => document.getElementById(id);
const maxInput = 128 << 20;
let fixtureText, fixture, build, current;
let links = [];
window.sprBenchmark = { status: "loading" };

function status(text, error = false) {
  $("status").textContent = text;
  $("status").classList.toggle("error", error);
}

function finish(job) {
  if (job !== current) return;
  job.worker?.terminate();
  clearInterval(job.timer);
  current = null;
  $("cancel").disabled = true;
  $("start").disabled = false;
}

function fail(job, message) {
  if (job !== current) return;
  window.sprBenchmark = { status: "error", error: message };
  status(message, true);
  finish(job);
}

async function initialize() {
  if (!window.isSecureContext || !crypto.subtle || typeof Worker !== "function") {
    throw new Error("Use HTTPS or localhost in a browser with WebAssembly and workers");
  }
  fixtureText = new TextDecoder("utf-8", { fatal: true }).decode(await sprFetch("fixture.json", 64 << 10));
  fixture = JSON.parse(fixtureText);
  build = JSON.parse(new TextDecoder().decode(await sprFetch("build.json", 64 << 10)));
  if (fixture.format !== "spr-browser-rehearsal-v1" || !fixture.insecure_test_setup || fixture.track !== "core") {
    throw new Error("This prototype accepts marked core rehearsal parameters only");
  }
  if (!Number.isSafeInteger(fixture.input.size) || fixture.input.size <= 0 || fixture.input.size > maxInput) {
    throw new Error("Input exceeds the browser prototype limit");
  }
  if (build.format !== "spr-browser-build-v1" || build.engine !== fixture.engine) throw new Error("Engine build mismatch");
  $("circuit-title").textContent = fixture.circuit;
  $("size").textContent = `${(fixture.input.size / 1e6).toFixed(1)} MB input`;
  $("description").textContent = "One circuit, one local contribution, no uploads.";
  $("build").textContent = `Engine: ${build.engine}. Go: ${build.go_version}. Source: ${build.source_revision || "unrecorded"}${build.source_modified ? " (modified checkout)" : ""}. Asset hashes detect corruption; they do not authenticate a compromised website.`;
  status("Ready. Keep this tab open while the worker runs.");
  $("start").disabled = false;
  window.sprBenchmark = { status: "ready" };
}

$("start").addEventListener("click", async () => {
  if (current) return;
  for (const link of links) URL.revokeObjectURL(link);
  links = [];
  $("results").hidden = true;
  $("start").disabled = true;
  $("cancel").disabled = false;
  $("progress").value = 0;
  const job = { controller: new AbortController(), started: performance.now(), stages: [] };
  current = job;
  window.sprBenchmark = { status: "running" };
  job.timer = setInterval(() => {
    $("elapsed").textContent = `${Math.round((performance.now() - job.started) / 1000)} seconds elapsed`;
  }, 250);
  try {
    status("Downloading public parameters…");
    const input = await sprFetch("input.ph2", maxInput, fixture.input.size, count => {
      $("progress").value = count * 100 / fixture.input.size;
    }, job.controller.signal);
    if (current !== job) return;
    job.downloadSeconds = (performance.now() - job.started) / 1000;
    $("progress").removeAttribute("value");
    job.worker = new Worker("worker.js");
    job.worker.onerror = event => fail(job, event.message || "Browser worker failed");
    job.worker.onmessage = ({ data }) => {
      if (current !== job) return;
      if (data.type === "error") return fail(job, data.error);
      if (data.type === "stage") {
        job.stages.push(data);
        window.sprBenchmark.stage = data.stage;
        const labels = { load_engine: "Loading and checking the contribution engine…", decode: "Checking and decoding public parameters…", contribute: "Computing your contribution…", verify: "Verifying the contribution locally…", encode: "Encoding the public result…", complete: "Preparing the result…" };
        status(labels[data.stage] || data.stage);
        return;
      }
      if (data.type !== "complete") return;
      const report = { ...data.report, download_seconds: job.downloadSeconds, total_seconds: (performance.now() - job.started) / 1000,
        browser: navigator.userAgent, stages: job.stages, build, native_verification_passed: false,
        memory_note: "WASM linear memory capacity; excludes browser, JavaScript buffers, and process overhead." };
      const blob = new Blob([data.output], { type: "application/octet-stream" });
      const reportBlob = new Blob([JSON.stringify(report, null, 2) + "\n"], { type: "application/json" });
      $("save-output").href = URL.createObjectURL(blob);
      $("save-report").href = URL.createObjectURL(reportBlob);
      links = [$("save-output").href, $("save-report").href];
      $("results").hidden = false;
      $("progress").value = 100;
      $("result-summary").textContent = `Local verification passed. Engine: ${report.engine_seconds.toFixed(2)} s. Worker memory capacity: ${(report.wasm_memory_bytes / 2 ** 20).toFixed(0)} MiB.`;
      status("Rehearsal complete. Save the result for native verification.");
      window.sprBenchmark = { status: "complete", report };
      finish(job);
    };
    job.worker.postMessage({ fixtureText, input: input.buffer, build }, [input.buffer]);
  } catch (error) {
    if (current === job) fail(job, error.message || String(error));
  }
});

$("cancel").addEventListener("click", () => {
  if (!current) return;
  const job = current;
  job.controller.abort();
  finish(job);
  $("progress").value = 0;
  status("Cancelled. Run again to start a fresh contribution.");
  window.sprBenchmark = { status: "cancelled" };
});

initialize().catch(error => {
  window.sprBenchmark = { status: "error", error: error.message };
  status(error.message, true);
});
