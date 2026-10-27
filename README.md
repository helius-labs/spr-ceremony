# SPR ceremony prototype

Groth16 BN254 Phase 2 ceremony tooling for Zolana. The contributor binary handles
downloads, local contributions, uploads, and acceptance receipts. The coordinator
keeps an independent transcript for every circuit.

| Track | Scope | Circuits |
| --- | --- | ---: |
| `core` | 38 confidential transfer shapes; standard merge with 8, 24, or 52 inputs | 41 |
| `batch-4999` | Address-append, tree height 40, batch size 4999 | 1 |

Custom-ring variants and batch 10 are excluded. `core` is the default; every
large-track operation requires `--track batch-4999`. Different tracks can use
separate coordinator directories, servers, participants, and schedules.

**This is a prototype for rehearsal and integration testing.** The full 4999
circuit has not been benchmarked. The engine loads parameter vectors into memory;
separate scheduling does not reduce that circuit's memory requirement. Read
[the limitations](#prototype-boundaries) before arranging a public ceremony.

## Build

Go 1.27.1 is pinned to match the current Zolana toolchain. Linux and macOS are
supported; the coordinator uses Unix file locks.

```sh
go build -o spr-ceremony ./cmd/spr-ceremony
go test -race ./...
```

Participants need only the compiled `spr-ceremony` binary and an invitation JSON
file. Building from source requires Go. There are no published release binaries
yet.

## Try a local ceremony

Initialize a rehearsal and start the coordinator:

```sh
./spr-ceremony demo --out rehearsal
./spr-ceremony serve --dir rehearsal
```

`demo` creates tiny plain and commitment-bearing circuits in `core`, plus a tiny
circuit in the large track to exercise scheduling. **That third circuit is not
batch 4999.** The manifest and every export carry `insecure_test_setup: true`.

In another terminal:

```sh
./spr-ceremony invite --dir rehearsal --name alice --out invitations/alice.json
./spr-ceremony contribute --invitation invitations/alice.json

./spr-ceremony invite --dir rehearsal --name bob --out invitations/bob.json
./spr-ceremony contribute --invitation invitations/bob.json
./spr-ceremony status --dir rehearsal
```

The same contribution command can be rerun after a disconnect. It resumes partial
downloads, reuses completed local outputs, and retries uploads. Repeated accepted
uploads return the original receipt without adding a second contribution.

Exercise the separate track:

```sh
./spr-ceremony invite --dir rehearsal --name large-alice \
  --track batch-4999 --out invitations/large-alice.json
./spr-ceremony contribute --invitation invitations/large-alice.json \
  --track batch-4999
```

To replay the core transcript, use the manifest digest printed by `demo`:

```sh
./spr-ceremony audit --dir rehearsal --manifest-sha256 <manifest-sha256>
```

Export and independently reproduce a key snapshot:

```sh
# This fixed beacon is ONLY for the rehearsal.
./spr-ceremony export --dir rehearsal --out export/rehearsal \
  --beacon 4242424242424242424242424242424242424242424242424242424242424242
./spr-ceremony verify-export --dir rehearsal --export export/rehearsal \
  --manifest-sha256 <manifest-sha256>
```

An export records its exact accepted head, contribution count, beacon, and key
hashes. It remains verifiable if later contributions are accepted. Exporting does
not close the ceremony or deploy keys.

## Zolana circuit adapter

The optional adapter compiles Zolana's actual circuit constructors with their
existing compiler settings. The participant binary has no Zolana dependency.
Build against an explicit clean checkout:

```sh
bash scripts/build-zolana.sh /path/to/zolana
./spr-zolana compile --track core --out ceremony-data/core-r1cs
```

For the first integration exercise, compile just one circuit:

```sh
./spr-zolana compile --circuit transfer_confidential_1_2 \
  --out ceremony-data/transfer-r1cs
```

The output includes `compilation.json` with the source commit, circuit parameters,
constraint/domain sizes, and R1CS SHA256s. Production preparation requires these
metadata to match. The build helper checks the selected gnark dependencies against
the ceremony engine's pins.

Batch 4999 has an independent compilation command. Run it only after arranging
appropriate resources:

```sh
./spr-zolana compile --track batch-4999 --out ceremony-data/batch-r1cs
```

### Supply Phase 1 parameters

Reuse an independently verified public Powers of Tau ceremony. The bridge accepts
the **compressed `.ph1` format** emitted by `semaphore-mtb-setup p1i`, not a raw
`.ptau` file. Authenticate the original Powers of Tau artifact and the conversion
before trusting its `.ph1` digest.

```sh
./spr-ceremony import-ph1 --input verified.ph1 \
  --sha256 <authenticated-ph1-sha256> \
  --provenance '<public ceremony, source digest, converter revision and verification record>' \
  --out ceremony-data/public.srs
```

The importer checks the digest, points, and algebraic consistency. It does not
replay the public Phase 1 contribution transcript or prove that the operator's
provenance statement is true. Preserve that verification record separately.

For local testing only, `test-srs --power N --out ...` generates an explicitly
marked rehearsal SRS. Its metadata marker propagates through preparation and
export. The Zolana packaging command refuses rehearsal exports.

### Prepare and run a track

```sh
./spr-ceremony prepare --track core \
  --r1cs-dir ceremony-data/core-r1cs \
  --source-revision <commit-from-compilation.json> \
  --srs ceremony-data/public.srs --out ceremony-data/core
./spr-ceremony serve --dir ceremony-data/core --listen 127.0.0.1:8080
./spr-ceremony invite --dir ceremony-data/core --name alice \
  --url https://ceremony.example.org --out invitations/alice.json
```

Place the server behind HTTPS for remote use. The client permits cleartext HTTP
only on loopback. Deliver the invitation and binary through an authenticated
channel: the invitation pins the manifest digest and input hashes, and its token
authorizes uploads. Invitations default to 24 hours and can be issued with `--ttl`.

The coordinator does not give contributors cloud credentials. Participants receive
only the changing Phase 2 objects; static SRS and R1CS data are used during
initialization, transcript audit, and key extraction.

`--circuit` on `prepare` allows a subset rehearsal or a separate campaign for one
circuit. Completeness is checked against that campaign's manifest, so a subset
campaign must not be presented as covering the entire 41-circuit core track.

### Export Zolana keys

Choose and announce a public beacon source/time before closing participation,
following the upstream gnark sealing protocol. The CLI accepts its hex bytes; it
does not fetch or authenticate a beacon.

```sh
./spr-ceremony export --dir ceremony-data/core --out export/core \
  --beacon <public-beacon-hex>
./spr-zolana package --ceremony ceremony-data/core --export export/core \
  --out export/zolana-keys
```

Packaging replays the transcript and reproduces the exported keys before wrapping
them with the exact frozen R1CS into Zolana's `.key` format. It checks the source
revision and refuses test setups. Regenerating Rust verification keys, SDK hashes,
and `proving-keys.lock`, publishing artifacts, and deploying remain separate
release work. Do not run Zolana's current key-rotation setup steps over ceremony
keys: those steps generate a fresh local setup.

## Independent verification and storage

The coordinator directory contains:

```text
manifest.json          frozen circuit, source, engine and Phase 1 metadata
objects/<sha256>       immutable public R1CS, SRS and Phase 2 objects
state/<circuit>.json   accepted head and ordered receipts
invitations/           PRIVATE upload tokens; never publish this directory
```

Public HTTP endpoints expose `/v1/manifest`, `/v1/status`, and
`/v1/objects/<sha256>`. An auditor needs the authenticated manifest, the selected
track's state files, and every referenced object. `audit` reconstructs each initial
state and verifies every accepted transition; `verify-export` additionally
reproduces the recorded key snapshot. Archive these public files, not the entire
coordinator directory containing invitations.

Contributions are serialized per circuit using filesystem locks and an atomic
head update. Invitations snapshot the current heads. If two contributors start
from the same head, only the first valid submission advances it; the other needs
a new invitation. The prototype has no automatic queue/reservation service.

## Validation

The root tests exercise HTTP contribution, commitment-bearing proofs, reproducible
exports, malformed inputs, conflicting branches, resumable downloads, receipt
recovery, and transcript/key tampering.

Optional integration checks use valid witnesses from the pinned Zolana checkout:

```sh
SPR_VECTORS=/absolute/path/to/vectors \
  bash scripts/test-zolana.sh /path/to/zolana
bash scripts/test-solana.sh /path/to/groth16-solana /absolute/path/to/vectors
```

These checks run a confidential 1×2 transfer and standard 8-input merge through
two contributions, key export, and gnark proof verification. The Rust harness then
checks those proofs with `groth16-solana`, including rejection of changed public
inputs. The fixture generator uses Go overlays and does not edit Zolana source.
Set `SPR_TEST_SRS` to a previously generated power-18 rehearsal SRS to reuse it.

## Prototype boundaries

- Batch 4999 is cataloged and isolated, but its initialization, contribution,
  verification, extraction, and hardware requirements remain unmeasured.
- Upstream MPC operations use full in-memory vectors. The default `--max-domain`
  is `2^22`; raising it explicitly is not a memory-suitability check. Contributors
  also have a default 1 GiB input-file limit (`--max-file-bytes`).
- Downloads resume by byte range. Interrupted uploads restart from the beginning;
  accepted uploads are idempotent. Completed local contribution outputs are
  reused. Interrupted computation restarts with fresh randomness; secrets are
  never checkpointed to disk by this tool.
- There is no automatic queue, invitation renewal, signed release distribution,
  deployment automation, or public infrastructure in this prototype.
- The coordinator assumes a trusted local filesystem and bounded rehearsal
  traffic. Internet deployment needs authentication/rate-limit/resource review
  and external cryptographic review, including the Phase 1 conversion and beacon
  procedure, before a production ceremony.

The cryptographic engine is gnark `v0.16.4` with gnark-crypto
`v0.21.1-0.20260913224755-1409fe592052`. Its Phase 2 format differs from the old
Semaphore fork; existing `.ph2` contributions are not imported.
