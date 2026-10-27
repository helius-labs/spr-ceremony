# Zolana core trusted setup

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

Participants can verify their saved input/output pairs offline and publish a
small attestation, following the useful participant-controlled audit flow in
[BMT's ceremony](https://github.com/lightprotocol/bmt-setup-ceremony):

```sh
./spr-ceremony verify-contribution --invitation invitations/alice.json \
  --work work --out alice-attestation.json
```

The attestation includes public hashes and receipts, never the invitation token
or signed URLs. Compare its receipts against the published final transcript.
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

## Source and release integration

```sh
bash scripts/build-zolana.sh /path/to/clean/zolana
./spr-zolana compile --track core --out core-r1cs
```

The adapter uses Zolana's real constructors and records the source revision,
R1CS hashes, domains, and constraints. Production preparation requires matching
compilation metadata. The cryptographic engine is pinned to gnark `v0.16.4` and
gnark-crypto `v0.21.1-0.20260913224755-1409fe592052`.

The Phase 1 bridge imports the legacy converter's compressed `.ph1` format.
It checks encoding and algebraic consistency; it does not replay the original
public transcript. [The Phase 1 procedure](docs/operations.md#phase-1-and-circuit-freeze)
verifies the public transcript before conversion.

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
The manual GitHub Actions binary workflow produces downloadable build artifacts;
it does not publish a GitHub release. The workflow signs build provenance with
GitHub artifact attestations. After downloading a workflow-built binary, verify
its repository provenance with `gh attestation verify PATH_TO_BINARY --repo
helius-labs/spr-ceremony`, and check the source revision before running it. Locally
built binaries have checksums and embedded build metadata, but no GitHub signature.

Prototype directories created before checkpoints were introduced must be
recreated. Old Semaphore `.ph2` contributions are not compatible with this engine.
