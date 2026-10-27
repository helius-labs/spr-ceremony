# Solana Privacy Rings · Trusted setup

Groth16 BN254 Phase 2 tooling for **41 core circuits**: 38 confidential-transfer
shapes and standard merges with 8, 24, or 52 inputs. Custom rings and batch 10 are
excluded. Batch 4999 is deferred; its separate prototype track is not covered by
the production opening/finalization workflow.

Participants run one command. The coordinator assigns an available circuit,
renews its reservation, verifies the contribution, and publishes a durable
checkpoint before returning an acceptance receipt. Downloads can go directly
from private S3 through short-lived URLs. Participants need no AWS credentials.

**Public launch still requires independent cryptographic/security review and an
AWS deployment/restore drill.** Engineering tests are not an external audit. See
[launch gates and evidence](docs/readiness.md) and the [operator runbook](docs/operations.md).

## Build and rehearse

Go 1.27.3 includes security fixes over Zolana’s original 1.27.1 toolchain;
the gnark versions remain pinned. Linux and macOS are supported;
the coordinator uses Unix file locks and must have one active instance.
Participants need only the compiled binary. Normal Phase 1 preparation also
runs inside that binary; source builds require Go, with modules fetched
automatically. Independent historical transcript replay and Zolana release
development have additional tools described in the runbook.

```sh
go build -o spr-ceremony ./cmd/spr-ceremony
go test -race ./...
./spr-ceremony demo --out rehearsal
./spr-ceremony serve --dir rehearsal
```

In another terminal:

```sh
./spr-ceremony invite --dir rehearsal --name alice --out invitations/alice.json
./spr-ceremony contribute --invitation invitations/alice.json
./spr-ceremony status --dir rehearsal
```

Send invitations privately. Give every contributor a stable, unique pseudonym.
Rerun `contribute` after interruption: downloads resume, completed local outputs
are reused, and already accepted circuits are skipped. Invitations default to
24 hours; renew an expired invitation using the same name. There is no need to
manually refresh an invitation after another contributor advances a circuit.
A contribution interrupted during computation starts that computation again.
If acceptance succeeded before the client stopped, rerunning also recovers a
missing receipt after checking its saved input/output files. Completed queue
invitations can recover receipts after closure while the invitation remains valid.

Participants can verify their saved input/output pairs offline and publish a
small attestation, following the useful participant-controlled audit flow in
[BMT's ceremony](https://github.com/lightprotocol/bmt-setup-ceremony):

```sh
./spr-ceremony verify-contribution --invitation invitations/alice.json \
  --work work --out alice-attestation.json
```

The attestation includes public hashes and receipts, never the invitation token
or signed URLs. Check that every attested receipt appears in an independently authenticated
checkpoint:

```sh
./spr-ceremony verify-attestation --dir recovered-core --attestation alice-attestation.json \
  --manifest-sha256 AUTHENTICATED_MANIFEST --checkpoint-sha256 AUTHENTICATED_CHECKPOINT
```

Run `audit` separately to verify the full MPC transcript.
It is not an identity signature or a substitute for a full ceremony audit.

`demo` uses tiny circuits and explicitly insecure parameters. Its extra scheduling
fixture is **not** the real 4999 circuit. Test markers propagate into exports;
production packaging rejects them. Tiny rehearsal snapshots may use a fixed
beacon with `export`; real campaigns must use `open`, `close`, and `finalize`.

## Storage and phases

```text
verified Phase 1 + pinned circuit compilation
                   ↓
               prepare → sync to S3
                   ↓
       open with an announced future beacon
                   ↓
       queued contributions → verified checkpoints → S3
                   ↓
       close and publish frozen heads before beacon
                   ↓
       verify beacon → finalize → archived proving/verifying keys
                   ↓
       independently verify → package → stage Zolana release
```

S3 contains content-addressed public artifacts: full and circuit-sized Phase 1
parameters, R1CS, initial and contributed Phase 2 files, checkpoint history, and
final keys. Immutable objects are uploaded first; a conditional pointer update
commits the checkpoint. An outage prevents acceptance, and a stale coordinator
cannot overwrite a different published head. Invitations and reservations stay
local. [Terraform](infra/s3/main.tf) provisions a private, encrypted, versioned
bucket and a coordinator policy without delete permission.

```json
{"bucket":"YOUR-CEREMONY-BUCKET","region":"YOUR-REGION","prefix":"core/v1"}
```

```sh
./spr-ceremony sync --dir ceremony-data/core --s3-config s3.json --out recovery.json
./spr-ceremony serve --dir ceremony-data/core
```

Successful `sync` saves the storage configuration locally. Later accepted
contributions and phase transitions sync automatically. Checkpoint references
must also be witnessed outside the coordinator; S3 hashes alone do not establish
when a beacon plan or closure was announced. Recovery instructions are in the
[runbook](docs/operations.md#recovery).

## Hosted participant UI

The [hosted UI](docs/hosted-ui.md) accepts private invitations and joins the same
core queue as the native client. It computes in a browser worker, renews leases,
resumes public downloads, retains completed output through retries/reloads, and
checks accepted receipts against published checkpoints. Participants need only a
browser, or can download the native client and their invitation from the site.

```sh
export GOTOOLCHAIN=go1.27.3
bash scripts/build-browser.sh
./spr-ceremony serve --dir rehearsal --web-dir dist/browser \
  --public-url http://127.0.0.1:8080
```

In a second terminal:

```sh
./spr-ceremony invite --dir rehearsal --name alice \
  --out invitations/alice.json --link-out invitations/alice.link
```

Open the private link or choose the invitation file at `http://127.0.0.1:8080`.
The guide covers HTTPS deployment, native binary distribution, optional direct S3
downloads, recovery, and remaining launch checks. Browser assets build with Go
and a shell; there is no npm install or external runtime script.

The [standalone rehearsal](docs/browser-prototype.md) remains available for a
single-circuit benchmark and independent verification without joining a queue.

## Source and release integration

```sh
bash scripts/build-zolana.sh /path/to/clean/zolana
./spr-zolana compile --track core --out core-r1cs
```

The adapter uses Zolana's real constructors and records the source revision,
R1CS hashes, domains, and constraints. Production preparation requires matching
compilation metadata. The cryptographic engine is pinned to gnark `v0.16.4` and
gnark-crypto `v0.21.1-0.20260913224755-1409fe592052`.

Prepare the fixed public core Phase 1 parameters with one command:

```sh
./spr-ceremony phase1
```

It downloads/resumes the public power-21 transcript, authenticates its SHA256,
published BLAKE2b and size, converts it in Go, and checks the imported SRS against
the previously verified reference. The result is
`ceremony-data/phase1/public-21.srs` with provenance metadata. This path requires
no Node.js, Python, or separate converter checkout. Use `--input FILE.ptau` to
reuse a local copy of the same transcript.

This checks encoding and algebraic consistency; it does not replay the original
historical contributions. [The independent audit procedure](docs/operations.md#independent-historical-transcript-replay)
retains full snarkjs transcript replay and independent conversion. The lower-level
`import-ph1` bridge also remains available for reviewed conversion workflows.

After finalization, `spr-zolana package` wraps the ceremony keys in Zolana's
existing `.key` format without running a new setup. The
[release staging helper](scripts/stage-core-release.py) generates the 41 Rust
verifier modules, updates Go/TypeScript key pins, and writes an S3 publication
plan. It preserves excluded key hashes and never uploads or deploys a release.

## Validation and binaries

```sh
# Includes storage outage, response loss, corruption, and restore tests when set:
SPR_S3_ENDPOINT=http://127.0.0.1:5000 go test -race ./...
python3 -m unittest discover -s scripts -p 'test_*.py'

# Real Zolana witnesses, two contributors, packaging, and proof verification:
SPR_CORE_ALL=1 SPR_VECTORS=/tmp/core-vectors \
  bash scripts/test-zolana.sh /path/to/zolana
SPR_CORE_ALL=1 bash scripts/test-solana.sh /path/to/pinned/groth16-solana /tmp/core-vectors

# Clean committed source; Linux/macOS amd64 and arm64, with SHA256SUMS:
bash scripts/build-release.sh
```

The complete core rehearsal is expensive. `SPR_R1CS_DIR`, `SPR_TEST_SRS`, and
`SPR_REHEARSAL_DIR` allow reuse of compiled circuits, a marked test SRS, and
completed stages. See [evidence](docs/readiness.md) for measured scope and limits.
To recheck all saved checkpoints and their Phase 1 derivations without repeating
initialization, set `SPR_CORE_REHEARSAL` to the saved rehearsal directory and
`SPR_TEST_SRS` to its marked SRS, then run `go test -run '^TestCoreRehearsalBindings$'`.

CI also verifies the [41 saved public proof vectors](docs/evidence/core-vectors)
with the pinned Rust verifier and rejects changed public inputs. These use marked
test parameters; this check does not regenerate a ceremony or supply production keys.

The manual GitHub Actions binary workflow produces downloadable build artifacts;
it does not publish a GitHub release. The workflow signs build provenance with
GitHub artifact attestations. After downloading a workflow-built binary, verify
its repository provenance with `gh attestation verify PATH_TO_BINARY --repo
helius-labs/spr-ceremony`, and check the source revision before running it. GitHub downloads may need `chmod +x PATH_TO_BINARY` after verification. Locally
built binaries have checksums and embedded build metadata, but no GitHub signature.

Prototype directories created before checkpoints were introduced must be
recreated. Old Semaphore `.ph2` contributions are not compatible with this engine.
