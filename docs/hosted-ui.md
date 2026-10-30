# Hosted ceremony

The participant page offers one path: the native CLI. Open a private invitation
link, copy the terminal command, then copy the invitation into the client's
hidden prompt. Mac and Linux binaries require no compilation.

The page shows public contribution records, parameters and finalized keys. It
never contributes, reserves a circuit, uploads work, or starts a WebAssembly
worker. Invitation fragments are removed from the address bar and kept only in
memory for copying. The page does not send the invitation to the coordinator;
the CLI validates its signature, expiry and manifest pin before participating.
Reloading clears the invitation. Reopen the original private link when needed.

## Contribution recovery

For `invalid invitation link`, reopen the original private link and click **Copy
invitation**. Paste that value at **Invitation link (hidden)** in the terminal.
Do not copy the browser address bar: the page removes the private fragment after
opening. This error is a local format check, before any request to the coordinator;
reissuing the invitation is not needed just to correct a paste.

After upload, the client automatically waits up to ten seconds when a receipt
signature fails only its time-window check. It verifies the same receipt against
the actual local clock and never repeats the upload or computation during this
wait. Other signature failures stop immediately.

If the CLI still reports that a coordinator signature is outside its valid time window,
enable automatic date and time on your computer, then rerun the same command with
the same invitation. On macOS, use **System Settings → General → Date & Time →
Set time and date automatically**. Earlier clients report only `invalid coordinator
signature`; that message can have other causes too.

Keep the existing workspace. An upload may already have been accepted before
the client failed to verify its receipt; rerunning checks progress and skips
accepted circuits. If verification still fails, stop and contact the operator
with the circuit name and error, keeping the invitation private. Do not disable
signature checks or manually advance the clock to match an unverified message.

## Local test

```sh
go build -o spr-ceremony ./cmd/spr-ceremony
bash scripts/build-browser.sh
./spr-ceremony demo --out ceremony-data/web-demo
./spr-ceremony serve --dir ceremony-data/web-demo --web-dir dist/browser \
  --public-url http://127.0.0.1:8080
```

In another terminal:

```sh
./spr-ceremony invite --dir ceremony-data/web-demo \
  --url http://127.0.0.1:8080 --name alice \
  --out invitations/alice.json --link-out invitations/alice.link
./spr-ceremony contribute --invitation invitations/alice.json
```

This fixture has two test circuits. Its parameters are explicitly marked and are
unsuitable for production keys. Keep both invitation files private. The local
page shows manual CLI instructions; the one-line installer requires HTTPS and a
verified release supplied through `--native-dir`.

Open the link from `invitations/alice.link` to check invitation copying. Reload
the public page after the native contribution: **Contributions** should show
Alice's signed 2/2 acceptance record. Running the client again skips accepted
work. Stop it with Ctrl+C; use the same invitation to resume.

Independently verify the local transcript:

```sh
manifest_sha=$(shasum -a 256 ceremony-data/web-demo/manifest.json | awk '{print $1}')
./spr-ceremony audit --dir ceremony-data/web-demo --manifest-sha256 "$manifest_sha"
```

## Hosting and verification

Production requires a clean committed web build and matching native release.
The coordinator verifies asset hashes at startup and holds the checked assets
in memory. Restart deliberately to deploy a new release, after contributors have
finished or paused. Do not change the ceremony manifest or accepted work.

```sh
./spr-ceremony serve --dir ceremony-data/core \
  --listen 127.0.0.1:8080 --public-url https://ceremony.example.org \
  --web-dir dist/browser --native-dir dist/COMMIT
```

Use TLS, a dedicated origin and the original Host header. The public listener
exposes no admin API. Operator access uses the separate AWS-authenticated
[control panel](aws.md#invite-from-the-control-panel).

The one-line installer trusts its host. **Verify your download** provides pinned
hashes and build-attestation instructions; authenticate the source commit
independently. See [release verification](releases.md). Neither the CLI nor the
operating system guarantees secret memory erasure.

The native client handles queue reservations, renewal, checked downloads,
contribution verification, retries and receipts. S3 publication precedes
acceptance. A full transcript audit and final-key reproduction remain separate
from receipt authentication. See the [operator guide](operator-cli.md).

## Automated checks

```sh
python scripts/test-hosted-browser.py --binary ./spr-ceremony \
  --web-dir dist/browser --out /tmp/hosted-results
python scripts/test-admin-browser.py --binary ./spr-ceremony \
  --web-dir dist/browser --out /tmp/admin-results
```

These developer checks require Python, OpenSSL and the Playwright dependencies
in `scripts/browser-requirements.txt`. Participants need none of them. Chromium
and WebKit exercise the CLI-only page, private clipboard handoff, absence of
browser computation/storage, native contributions and reruns, public records,
mobile layout, partial native participation, invitation reissue and revocation.
Set `SPR_S3_ENDPOINT` to a loopback Moto endpoint to include native publication
through S3. The tests use separate marked fixtures and never the live ceremony.

The standalone offline WASM benchmark remains a development tool; it is not a
participation option on the hosted site. Historical browser measurements remain
in `docs/evidence/`. Linux WebKit checks do not certify Safari on a Mac.
