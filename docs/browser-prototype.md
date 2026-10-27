# Browser contribution rehearsal

This prototype runs the pinned Go Phase 2 contribution engine in a browser
worker. It authenticates the input, checks encoding and lengths before decoding,
contributes using Go's browser `crypto/rand` implementation, verifies the update,
and exports the public result. The native client and WebAssembly share the
implementation in `internal/contribution`; the gnark versions are unchanged.

It accepts **marked, insecure core rehearsal parameters only**. There is no
coordinator submission, participant signup, invitation handling, or queue UI.
The page downloads public data and code and uploads nothing. No sampled secret
is intentionally persisted, but stopping a worker is not proof of memory erasure.

## Try it on macOS or Linux

Building and previewing requires Go and a shell. The page itself requires only a
browser; there is no npm install, frontend framework, CDN, or analytics script.

```sh
export GOTOOLCHAIN=go1.27.3
bash scripts/build-browser.sh
go run ./cmd/spr-browser-rehearsal demo --out dist/browser-demo
go run ./cmd/spr-browser-rehearsal serve --dir dist/browser-demo
```

Open `http://127.0.0.1:8081`, click **Run rehearsal**, then save the contribution
and report. The demo includes a commitment-bearing circuit and finishes quickly;
it does not represent a real core circuit's resource requirements. The bundle
output must be new. Stop the preview server with Ctrl-C.

Independently verify the saved public output:

```sh
go run ./cmd/spr-browser-rehearsal verify --dir dist/browser-demo \
  --contribution /path/to/downloaded/contribution.ph2
```

The browser report records its own verification. The native command supplies a
separate check and rejects a changed challenge or a result for the wrong input.
Neither report claims acceptance into a live ceremony.

For a real core circuit, use an existing **marked rehearsal** directory produced
by `scripts/test-zolana.sh`. Preparation reads its initial public Phase 2 file;
it does not modify its checkpoints or contribution history:

```sh
go run ./cmd/spr-browser-rehearsal prepare \
  --dir /path/to/rehearsals/merge_52_1/ceremony --circuit merge_52_1 \
  --out dist/browser-merge52
go run ./cmd/spr-browser-rehearsal serve --dir dist/browser-merge52
```

The prototype caps input files at 128 MiB, enough for the measured core catalog.
Batch 4999 is outside this prototype. Transferring input to the worker avoids a
second main-page copy; terminating the worker on completion/cancellation releases
its runtime for reuse by the browser. Interrupted downloads or computation start
again; resumption belongs to the next integration stage.

## Hosted deployment

A prepared bundle is a static directory: HTML, JavaScript, WebAssembly, build
metadata, fixture metadata, and public input parameters. It can be hosted over
HTTPS without a local native process. The supplied Go server is a loopback-only
preview. For a hosted preview, reproduce its response headers, including:

```text
Content-Security-Policy: default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Embedder-Policy: require-corp
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Cache-Control: no-store
```

Serve `.wasm` as `application/wasm` and JavaScript with a JavaScript MIME type.
This does not turn the rehearsal into a production contributor. Asset hashes
detect corruption; hashes delivered by a compromised site do not authenticate
that site's code. Independent source/build verification and a native contributor
remain relevant to the threat model. The
[Go WebAssembly guide](https://go.dev/wiki/WebAssembly) documents the matching
runtime support file, and [W3C's security guidance](https://www.w3.org/TR/2017/REC-WebCryptoAPI-20170126/#security-considerations)
explains why malicious application code can compromise browser secrets.

## Queue integration after the prototype

The existing coordinator reserves **one contributor per circuit**. Different
circuits can have different active contributors. Each client processes one
reserved circuit at a time and requests another until all invited circuits are
covered. A possible order is:

```text
Circuit A: initial → Alice → Bob   → Carol
Circuit B: initial → Bob   → Carol → Alice
```

This is a lease-based scheduler, not a promise of one global FIFO position.
The hosted client would reuse `/v1/claim`, `/v1/heartbeat/{circuit}`,
`/v1/download/{hash}`, and `/v1/contributions/{circuit}`. Downloads use immutable
hash references. A heartbeat runs outside the computation worker. The coordinator
verifies the result against the reserved input and commits it durably to S3
before returning a receipt and releasing that circuit to the next contributor.
Checkpoint commits remain serialized even while contributors compute in parallel.

An abandoned reservation expires. Before resubmitting after interruption, the
client must reacquire a valid reservation and check its input hash. If the head
changed, it recomputes against the new head; a stale result cannot replace it.
Accepted circuits are tracked by contributor identity, allowing the same
participant to resume without starting all 41 again. The browser UI should show
that person's completed circuits and current job rather than a fictitious global
queue position. Native and browser clients can use the same protocol.

Remaining integration includes authenticated manifest/invitation handling,
heartbeat and tab-suspension recovery, resumable transfers, retained public
outputs/receipts, duplicate-tab handling, and S3 CORS for the intended hosted
origin. The shared engine alone does not implement those browser behaviors.

## Automated validation and measurements

The optional developer runner uses Playwright; those dependencies are not
required to build the browser assets or participate through a hosted page:

```sh
python3 -m venv /tmp/spr-browser-tools
/tmp/spr-browser-tools/bin/pip install -r scripts/browser-requirements.txt
/tmp/spr-browser-tools/bin/python -m playwright install --with-deps chromium webkit
go build -o /tmp/spr-browser-rehearsal ./cmd/spr-browser-rehearsal
/tmp/spr-browser-tools/bin/python scripts/benchmark-browser.py \
  --bundle dist/browser-demo --native /tmp/spr-browser-rehearsal \
  --browser chromium --out /tmp/browser-result
```

Use `--browser webkit` for Playwright WebKit or `--browser chrome` with an
installed Chrome channel. Linux WebKit is **not Safari**. Real macOS Safari
measurements require opening the prepared bundle on a Mac and saving its report;
verify its downloaded output with the native command above. Record the Safari
version and hardware alongside the report.

The runner checks native acceptance and rejection of a changed challenge. The
failure cases `--fault input-hash`, `length-prefix`, `entropy`, and `cancel`
exercise rejection before unsafe decoding, absence of a randomness fallback,
and cancellation. CI runs the commitment-bearing fixture in Chromium and WebKit
and uploads a static demo bundle plus results as a workflow artifact.

Engine timing includes input authentication, decoding, contribution, local
verification, encoding, and output copying/hashing; the initial JS-to-Go copy is
outside that timer. Download and overall page durations are separate. Reported
WASM memory is linear-memory capacity, **not total browser RSS**. Measurements on
localhost do not measure WAN transfers, queue waits, S3 publication, or mobile
capacity. The prototype does not establish production readiness.
