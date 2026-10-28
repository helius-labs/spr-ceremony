# Solana Privacy Rings · Hosted ceremony

The hosted UI runs the existing Go contribution engine in a WebAssembly worker
and joins the same per-circuit queue as the native client. The initial release
uses private invitations without accounts. Core only: batch 4999 is deferred;
custom rings and batch 10 remain excluded.

Participants need a current desktop browser. The native option needs only the
platform binary or the hosted installer. Building the browser bundle requires Go
and a shell, with no Node, npm, CDN, framework, or external runtime scripts.

## Local rehearsal

From the repository root:

```sh
export GOTOOLCHAIN=go1.27.3
go build -o spr-ceremony ./cmd/spr-ceremony
bash scripts/build-browser.sh
./spr-ceremony demo --out ceremony-data/web-demo
./spr-ceremony serve --dir ceremony-data/web-demo --web-dir dist/browser \
  --public-url http://127.0.0.1:8080
```

In a second terminal:

```sh
./spr-ceremony invite --dir ceremony-data/web-demo \
  --url http://127.0.0.1:8080 --name alice \
  --out invitations/alice.json --link-out invitations/alice.link
```

Open `http://127.0.0.1:8080` and choose `invitations/alice.json`, or open the link
stored in `invitations/alice.link`. Click **Contribute**. This small
rehearsal is clearly marked and cannot produce production keys. Use a new output
directory for another demo. Both invitation files are private, mode 0600.

The fragment in the link contains the bearer token and manifest pin; fragments
are not sent in HTTP requests. The page removes the fragment from browser history
and keeps credentials in memory. Reopen the original link or invitation file
after a reload. Do not put invitation tokens in query strings, analytics, logs,
public screenshots, or tickets. Authenticate the manifest pin through the private
invitation delivery channel and an independently published announcement.

## Manual test on a Mac

Use the small local rehearsal first, including Safari on the actual Mac. It has
**two** core test circuits, not the real 41-circuit catalog. A successful browser
run shows **2 / 2 accepted** and enables **Save receipts**.

1. Finish a contribution in Safari and save its receipts. Reload the page, choose
   the same invitation file again, and confirm it still shows **2 / 2 accepted**.
   The token is deliberately not retained across reloads. Reopening must not add
   another contribution.
2. In another terminal, verify the whole core transcript independently:

   ```sh
   manifest_sha=$(shasum -a 256 ceremony-data/web-demo/manifest.json | awk '{print $1}')
   ./spr-ceremony audit --dir ceremony-data/web-demo --manifest-sha256 "$manifest_sha"
   ```

   Expect `Transcript verified: core`.
3. Create a second invitation to test the native option:

   ```sh
   ./spr-ceremony invite --dir ceremony-data/web-demo \
     --url http://127.0.0.1:8080 --name bob --out invitations/bob.json
   ```

   Reload the page, choose `invitations/bob.json`, and open **Use terminal**.
   For this local test, use the binary you already built:

   ```sh
   ./spr-ceremony contribute --invitation invitations/bob.json --work work/web-native
   ```

   Keep the server running in the first terminal. Click **Refresh** in
   the browser; Bob should now show **2 / 2 accepted**. Run the audit again.
   The local rehearsal has no binary downloads unless `--native-dir` is set;
   the already built `spr-ceremony` is sufficient for testing the handoff.
4. Repeat with a fresh pseudonym in another browser. Accepted contributors are
   identified by unique invitation ID, so reusing Alice's invitation will correctly show
   completed work instead of creating a new participant.
5. For a manual pause or reload during work, use browser network throttling or a
   real core rehearsal: the toy circuits finish too quickly for reliable manual
   interruption. **Pause** should release the lease and **Resume** should finish.
   Closing/reloading loses in-progress computation; completed public work can be
   reused only if its parent is still current. Automated CI covers these cases.

Stop the server with Ctrl-C. Restarting `serve` with the same directory preserves
accepted work; `demo` must be given a new output directory for a fresh campaign.
This local check does not exercise WAN/TLS, actual AWS permissions, or full core
resource requirements. Those remain separate staging tests.

## Full core browser rehearsal

Reuse untouched prepared core parameters to make a separate signed test campaign.
The helper checks every reused public object, marks the new manifest as insecure,
and generates a fresh coordinator key. It never opens or changes the prepared
source campaign and refuses an existing destination or contributed source.

```sh
python3 scripts/prepare-browser-rehearsal.py --binary ./spr-ceremony \
  --from-prepared ceremony-data/core --out rehearsal/browser-core
python3 scripts/test-hosted-core.py --binary ./spr-ceremony \
  --dir rehearsal/browser-core --web-dir dist/browser \
  --browser chromium --reopen --out rehearsal/browser-results
```

The second command requires the tools in `scripts/browser-requirements.txt` and
the matching Playwright browser installation. It runs the full hosted queue,
pauses and reloads after acceptance, resumes the same invitation ID, and verifies
all saved receipts with the native binary. Allow several hours; use a separate
test campaign and results directory for `--browser webkit`. Linux WebKit does
not establish Safari compatibility on a Mac.

Results contain a **private invitation file** alongside public reports. Keep the
directory private; publish only the report, receipts and checked screenshots.
To check completed receipt recovery and identical-output retries, reuse that
file with `--recover --invitation PATH`, omit `--reopen`, and choose a new results
directory. Keep the original loopback port available. These tests use local
networking; real AWS, laptop suspension and storage-pressure drills remain separate.

## Finish the local rehearsal

After both participants finish, save receipts and stop the server with Ctrl-C.
Audit and reproduce a test export:

```sh
manifest_sha=$(shasum -a 256 ceremony-data/web-demo/manifest.json | awk '{print $1}')
./spr-ceremony audit --dir ceremony-data/web-demo --manifest-sha256 "$manifest_sha"
./spr-ceremony export --dir ceremony-data/web-demo --out export/web-demo \
  --beacon 4242424242424242424242424242424242424242424242424242424242424242
./spr-ceremony verify-export --dir ceremony-data/web-demo --export export/web-demo \
  --manifest-sha256 "$manifest_sha"
```

The fixed beacon here is **test data**. The export is explicitly marked insecure;
these keys must never be used for production. New demos contain only two core
circuits. Older demos also contain an excluded-track scheduling fixture, so their
`close` requires more than the two core circuits. Recreate those demos to use the
new scope; never change a frozen manifest. The test export above does not claim
a production lifecycle closure.

For a real core-only campaign, [close and finalize](operations.md#close-finalize-and-independently-verify)
freezes all 41 heads before the preannounced beacon round. Then verify the signed
beacon, finalize, independently audit the transcript, and reproduce the exported
keys. Valid-witness and invalid-witness proof tests against the native and Solana
verifiers check circuit behavior separately; a correct ceremony does not prove
that a circuit implements the intended application rules.

## Participant behavior

- One lease per circuit. Different contributors can compute on different circuits
  concurrently; the UI reports accepted circuits, not a global FIFO position.
- The manifest bytes must match the invitation's SHA256. Jobs must match that
  manifest and the reserved head in an authenticated checkpoint. The worker checks
  artifact hashes and bounded vector lengths before curve decoding.
- A main-thread heartbeat renews the lease independently of the computation
  worker. The client renews again before upload. Sleep, suspension, or an expired
  lease can require rejoining and recomputation on a new head.
- Public downloads use checked HTTP ranges in 4 MiB chunks. Completed chunks and
  one pending public output are saved in IndexedDB. Every completed input is
  hashed before use. Quota/storage errors stop work before submission.
- Completed public output is saved before upload. Failed uploads retry the whole
  saved output; uploads are not multipart-resumable. Lost acceptance responses are
  idempotent. A checkpoint check recovers an accepted receipt after a reload.
- A stale output is discarded when a different head is reserved. Computation is
  not resumable mid-step; a new run samples new randomness.
- **Pause** terminates the worker, cancels transfers, and attempts to release the
  reservation. A disconnected tab's lease expires automatically. Web Locks prevent
  duplicate work from the same browser profile; use one device/client at a time
  for an invitation.
- The coordinator verifies the update natively. With S3 configured, publication
  and its conditional checkpoint update complete before acceptance. The browser
  checks receipt fields against its own output and verifies receipt inclusion in
  the published immutable checkpoint/state objects.
- **Save receipts** exports the native attestation JSON format. It contains
  no bearer token. Receipts from another device count as accepted work, without
  claiming that this browser computed them. A final transcript audit remains
  separate from inclusion checking.

Only public work is persisted. Consumed parameter chunks and accepted pending
outputs are removed; the browser retains receipts and a checkpoint reference.
Browser storage can be evicted. Keep the original private invitation and save
public receipts outside the browser. Neither browser nor native execution proves
that secret material has been erased from operating-system memory.

## Native client handoff

**Use terminal** offers a one-line launcher with hidden invitation input and
independently verifiable release provenance. See [terminal instructions](releases.md).
The **Download manually** option also offers a private invitation JSON download:

```sh
chmod +x spr-ceremony-darwin-arm64
./spr-ceremony-darwin-arm64 contribute --invitation invitation.json
```

The website polls the published checkpoint and shows accepted progress. It does
not launch local programs or require a localhost bridge. Pause the browser before
switching clients. Native work uses the same invitation ID and skips already accepted
circuits; an unaccepted browser output is not imported into the native workspace.

Serve the four platform binaries by passing `--native-dir` pointing at a verified
bundle from the `Release` workflow. Its `build.json` revision must match
the web bundle. Startup verifies all hashes. This workflow also publishes an
attested browser archive. Verify release artifacts against the frozen commit:

```sh
gh attestation verify browser-COMMIT.tar.gz --repo helius-labs/spr-ceremony \
  --signer-workflow helius-labs/spr-ceremony/.github/workflows/binaries.yml \
  --source-digest COMMIT
```

The same command applies to each native binary. `COMMIT` is the full reviewed
source revision. Hashes delivered by this site alone do not authenticate a
compromised host. Review the independently published commit/provenance.

## Hosting and S3

Build from a clean, committed checkout for a production campaign. The server
refuses a modified/unrecorded production web build, checks all asset hashes at
startup, and holds the checked bytes in memory. Deploy new bundles by restarting
one coordinator. Do not replace JavaScript in a running deployment.

With an already prepared/open core ceremony and S3 configured:

```sh
./spr-ceremony serve --dir ceremony-data/core \
  --listen 127.0.0.1:8080 --public-url https://ceremony.example.org \
  --web-dir dist/browser --native-dir dist/COMMIT
```

Put TLS in front of this listener. Preserve the original Host header exactly;
forwarded-host headers are not trusted. Use a dedicated origin with no unrelated
applications or scripts. The handler supplies a restrictive CSP, no-referrer,
no-store for private metadata/code, MIME protections, cross-origin isolation,
and HSTS on HTTPS deployments. API requests from other origins are rejected.
There is no public admin API, public signup, or cookie authentication. The
[private control panel](aws.md#invite-from-the-control-panel) uses a separate
loopback listener accessed through SSM.

Configure proxy request limits to accommodate a 128 MiB input plus the small
contribution overhead, and response timeouts to accommodate upload/verification
and remote publication. Keep the listener private, set proxy request-rate and
connection limits, and avoid logging Authorization headers. Coordinate restart
and restore with the existing [operations runbook](operations.md). The health
endpoint does not verify S3 availability.

By default browsers download public artifacts from the coordinator origin, even
when acceptance is backed by S3. This requires no S3 browser CORS configuration.
For direct downloads:

1. Set Terraform `browser_origins = ["https://ceremony.example.org"]` and apply it
   to the staging/production bucket through the operator's deployment process.
   This grants CORS for GET/HEAD and Range, exposing Content-Range, Content-Length,
   and ETag. It does not grant public access or browser upload permissions.
2. Add `--browser-download-origin https://BUCKET.s3.REGION.amazonaws.com`, using
   the exact origin returned by this deployment's presigned download tickets.
   This origin is added only to the page's connection policy. The computation
   worker keeps a same-origin connection policy.
3. Verify a contribution and interrupted range download against the deployed
   bucket. CORS must match the hosted origin and expose Content-Range. The client
   rejects redirects, unexpected origins, ranges, sizes, and hashes. Each retried
   chunk obtains a fresh ticket. Invitation/AWS credentials are never attached
   to storage requests.

The bucket stays private, encrypted, versioned, and conditionally written. The
browser sends public output to the coordinator, which verifies and archives it;
there are no presigned browser PUTs. See AWS's [CORS configuration documentation](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ManageCorsUsing.html).

## Validation and launch checks

CI runs Chromium and WebKit against a real local Go coordinator and the pinned
S3 emulator. It covers queued contributions, independent native transcript replay,
checkpoint inclusion, lost responses, reload/output reuse, stale heads, short
lease renewal, duplicate tabs, native handoff, altered receipts, pause/resume,
range recovery, manifest substitution, closed ceremonies, and S3 restore/audit.
The standalone rehearsal still checks malformed lengths, checksum failures,
missing cryptographic entropy, and cancellation.

For local hosted tests (Python/Playwright are developer-only dependencies):

```sh
python /path/to/scripts/test-hosted-browser.py --binary ./spr-ceremony \
  --web-dir dist/browser --out /tmp/hosted-results
```

Set `SPR_S3_ENDPOINT` to a loopback Moto endpoint to exercise presigned downloads
and restore. The test creates its own marked ceremony and emulator bucket.

[The hosted largest-core rehearsal](evidence/hosted-largest.json) records a
102.8 MB contribution accepted by the native coordinator and recovered from its
checkpoint. A Playwright large-body inspection limit interrupted the original
automation after acceptance; Blob upload and idempotent reupload were then checked.

[Measured browser results](evidence/browser-benchmarks.json) include real small
and largest core circuits. These are Linux Chromium/WebKit results, not macOS
Safari or mobile certification. WASM memory capacity is not total browser RSS.

Before a public launch: independent review/frozen source, a real AWS deployment
and recovery drill, and intended-device tests including macOS Safari, sleeping
and reopening a laptop, storage pressure, and the full 41-circuit session are
still required. This implementation and passing CI do not replace those checks
or the [core ceremony launch gates](readiness.md#release-gates).

Invitations and acceptance receipts carry coordinator OpenPGP signatures, checked
inside a dedicated Go WebAssembly verifier. The manifest pins its public key.
The control panel's **Reissue link** rotates access while preserving the public
invitation ID. A fresh invitation with a reused name starts new participation.
No personal GPG key or browser extension is required.
