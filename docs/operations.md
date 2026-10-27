# Core ceremony operations

## Phase 1 and circuit freeze

Freeze a clean Zolana revision, review the 41-circuit catalog, and compile with
`spr-zolana compile --track core`. The measured pinned catalog requires a power-21
SRS (largest domain 2,097,152). Compilation metadata binds source and R1CS bytes;
reviewers should reproduce those hashes from source.

Obtain the public power-21 Powers of Tau artifact from the
[snarkjs reference list](https://github.com/iden3/snarkjs/blob/master/README.md).
Authenticate its published BLAKE2b independently. Then:

```sh
bash scripts/verify-phase1.sh /path/to/powersOfTau28_hez_final_21.ptau \
  /path/to/semaphore-mtb-setup /new/path/verified-phase1
```

The helper checks the published hash, runs snarkjs 0.7.6 verification, builds the
converter at `577c91f3f8b8e6bccf9d2fbc629c9b18f5c56cad`, converts, and imports.
It records hashes of the transcript, verification log, npm lockfile, converter,
and converted file. Failed verification leaves logs but no usable SRS metadata.
Archive that directory, the original transcript, and the review record. The
coordinator archives imported parameters automatically; it does not automatically
upload the original `.ptau`, compiler source, or external review records.

Review the bridge and independently reproduce conversion. The CLI's provenance
string is an operator assertion, not a cryptographic proof of transcript review.
Never relabel a rehearsal SRS as production.

```sh
./spr-ceremony prepare --track core --r1cs-dir core-r1cs \
  --source-revision FROZEN_ZOLANA_COMMIT \
  --srs /path/verified-phase1/public-21.srs --out ceremony-data/core
```

Preparation is local and produces a `prepared` checkpoint. It cannot accept
contributions yet. Publish the manifest digest and source/Phase 1 provenance for
independent reviewers before opening.

## Storage and hosting

Provision [infra/s3](../infra/s3/main.tf) in the chosen AWS account. Terraform
requires explicit bucket/region variables and uses the operator's normal AWS
credentials. Inspect `terraform plan` before applying. Attach its coordinator
policy to the workload role; export `terraform output -raw s3_config` to a local
JSON file. Use a dedicated ceremony archive, independently from Zolana's release
key distribution bucket.

The configuration enables versioning, public-access blocks, AES256 encryption,
TLS-only access, immutable object creation, and conditional checkpoint writes.
The CLI checks versioning, encryption, and public-access blocks for production.
It cannot prove the entire IAM/bucket policy or account security posture.

```sh
./spr-ceremony sync --dir ceremony-data/core --s3-config s3.json --out prepared-recovery.json
```

Keep one coordinator on persistent local storage behind HTTPS. Bind the binary
to loopback and terminate TLS in a reverse proxy. Configure request/body limits,
rate limits, and upload/read timeouts for the measured maximum Phase 2 file;
disable proxy request buffering if it duplicates large uploads on disk. Do not
log authorization headers, invitation files, or presigned URL query strings.
The application offers no remote administration API.

```sh
./spr-ceremony serve --dir ceremony-data/core --listen 127.0.0.1:8080 \
  --max-uploads 2 --lease-ttl 30m --reserve-disk-bytes 2147483648
```

S3 transfer uses single PUTs capped at 5 GiB per artifact. This is a core-only
transport. Uploads go through the coordinator for verification; authenticated
artifact downloads use 15-minute presigned URLs that are refreshed on retry.
AWS credentials remain on the coordinator. `/healthz` checks local checkpoint
availability, not AWS reachability. Monitor S3 errors, free disk/RAM, process
restarts, accepted contribution counts, queue reservations, and beacon deadline.
`status` shows lifecycle and circuit histories. Keep logs of failures; there is
no built-in metrics backend, autoscaling, or high-availability coordinator.

Checkpoint commits serialize across processes. A long verification/publication
can delay scheduling. The heartbeat tolerates temporary failures within its
existing reservation. Test real bandwidth and choose a lease duration that
covers the longest commit with margin. Keep the contribution window shorter
than the announced beacon deadline by a generous closure margin.

## Open and participate

Use an independently authenticated drand chain-info JSON and chain hash for the
unchained RFC9380 G1 scheme (`bls-unchained-g1-rfc9380`). Select a specific future
round allowing time for contributions, closure, and independent witnesses. The
round must be more than one hour away at opening; normal campaigns need longer.

```sh
./spr-ceremony open --dir ceremony-data/core --chain-info drand-chain.json \
  --chain-hash AUTHENTICATED_CHAIN_HASH --round FUTURE_ROUND
./spr-ceremony checkpoint --dir ceremony-data/core --out opened-recovery.json
```

Publish the opened checkpoint digest, exact beacon network/round, and planned
closure schedule through an independently witnessed channel. A local timestamp
or a mutable S3 pointer alone does not prove that announcement preceded the
beacon. Do not modify an announced plan or restart with a different beacon.

```sh
./spr-ceremony invite --dir ceremony-data/core --url https://CEREMONY_HOST \
  --name alice --ttl 72h --out invitations/alice.json
# Alice, on a separate machine:
./spr-ceremony contribute --invitation alice.json --work alice-work
```

Give participants a binary checksum and authenticated manifest digest through a
trusted channel. They must protect their machine and entropy. The security
assumption is at least one honest participant per circuit whose randomness stays
unknown to attackers. The tool uses OS randomness and does not save the sampled
secret; it cannot attest to host compromise, swap, crash dumps, or secret erasure.
Use independent participants and review per-circuit participation before closure.
The software minimum of one accepted contribution is not a campaign policy.

If an invitation expires, reissue it with the same contributor name. A queue
invitation covers all core circuits, with one reserved job at a time. Expired
reservations are reclaimed. Re-running the client skips that person's accepted
circuits. A lost coordinator is recovered as below; do not run another writer
on a stale local copy.

Contributors can run `verify-contribution --invitation alice.json --work alice-work
--out alice-attestation.json` entirely offline, even after their invitation expires.
Retain the input/output pairs until verification. Publish only the generated
attestation (or its SHA256), through a channel under the contributor's control.
Reviewers can run `verify-attestation --dir recovered-core --attestation
alice-attestation.json --manifest-sha256 AUTHENTICATED_MANIFEST
--checkpoint-sha256 AUTHENTICATED_CHECKPOINT` to check every receipt and full-track
coverage. Missing or changed receipts fail. Run `audit` separately for the full
MPC chain. It proves a valid saved transition; it does not establish
participant identity, secret erasure, or inclusion by itself.

## Close, finalize, and independently verify

Stop issuing invitations, let active reservations complete or expire, and inspect
all 41 contribution histories. Close **before** the chosen beacon round:

```sh
./spr-ceremony close --dir ceremony-data/core
./spr-ceremony checkpoint --dir ceremony-data/core --out closed-recovery.json
```

Closure freezes the heads and refuses further contributions. It requires at least
one contribution per circuit and no live reservations. Publish and independently
witness the closure checkpoint before the beacon is revealed. If the deadline
is missed, the tool refuses a retroactive close; abandon that campaign and plan
a new one. There is no override to change the beacon after opening.

After the exact round is available, obtain its signed JSON. The endpoint is not
trusted for randomness: the signature, chain key, round, and optional randomness
field are checked locally.

```sh
./spr-ceremony finalize --dir ceremony-data/core --beacon-file signed-round.json \
  --out export/core
./spr-ceremony checkpoint --dir ceremony-data/core --out finalized-recovery.json
./spr-ceremony verify-export --dir ceremony-data/core --export export/core \
  --manifest-sha256 AUTHENTICATED_MANIFEST --checkpoint-sha256 AUTHENTICATED_FINAL_CHECKPOINT
```

Finalization replays contributions, seals all keys with the planned beacon, and
archives keys and export metadata before publishing the finalized checkpoint.
Repeating finalization is idempotent. Reviewers should restore into a separate
machine and run `audit` and `verify-export` with independently obtained digests.
Those checks cannot establish the real-world time of an announcement; retain the
external witness records for opening and closure.

## Recovery

Retain authenticated recovery files/checkpoint digests after phase transitions
and at intervals during participation. Public `/v1/checkpoint` lets independent
observers witness progress. S3's `campaigns/<manifest-sha>/latest.json` is the
committed head; only its reference changes. A recent independently witnessed
checkpoint is preferable to trusting an unverified mutable pointer.

Stop/fence the old coordinator first. Retrieve the latest committed pointer and
its immutable checkpoint, reconcile it with witnessed records, and authenticate
a recovery JSON containing `manifest` and `checkpoint` artifact references. An
older witnessed snapshot can be audited, but cannot resume writing over a newer
S3 head. Restore into a new directory:

```sh
./spr-ceremony restore --s3-config s3.json --recovery-file recovery.json \
  --recovery-sha256 AUTHENTICATED_RECOVERY_FILE_SHA256 --out recovered-core
./spr-ceremony audit --dir recovered-core --manifest-sha256 AUTHENTICATED_MANIFEST \
  --checkpoint-sha256 AUTHENTICATED_CHECKPOINT
./spr-ceremony sync --dir recovered-core --s3-config s3.json
```

Restore validates every artifact hash and checkpoint history. The separate audit
replays the MPC transcript. It intentionally does not restore invitation tokens,
leases, or S3 configuration; issue new invitations under the same contributor
names. A success receipt guarantees remote publication when S3 was configured.
If a crash occurred after the remote commit but before local acceptance, recover
the remote head: do not overwrite it or force the old coordinator to continue.

After finalized recovery, recreate the named key files without recomputing them:

```sh
./spr-ceremony fetch-export --dir recovered-core --out export/recovered
./spr-ceremony verify-export --dir recovered-core --export export/recovered \
  --manifest-sha256 AUTHENTICATED_MANIFEST --checkpoint-sha256 AUTHENTICATED_CHECKPOINT
```

Retain S3 object versions and external checkpoint witnesses. The coordinator role
has no delete permission. Account administrators remain capable of altering
policies or deleting data; external replicas and review records are an operational
responsibility. Do not prune content-addressed objects during a campaign.

## Stage the Zolana release

```sh
./spr-zolana package --ceremony ceremony-data/core --export export/core \
  --manifest-sha256 AUTHENTICATED_MANIFEST --checkpoint-sha256 AUTHENTICATED_FINAL_CHECKPOINT \
  --out packaged-core
cargo build --locked --manifest-path adapters/solana/Cargo.toml
python3 scripts/stage-core-release.py --package packaged-core \
  --package-sha256 AUTHENTICATED_PACKAGE_JSON_SHA256 --zolana /path/to/clean/zolana \
  --out staged-core-release
```

The package contains `.key`, raw `.vkbin`, and hashed metadata. Staging uses the
same verifier generator revision pinned by Zolana, preserves all excluded pins,
and writes only the 41 verifier modules plus the lockfile and TypeScript hash
table. It leaves the source checkout untouched.

`publication.json` lists core uploads and copies of unchanged S3-distributed keys
under the new immutable key-set prefix. Zolana currently uses one global prefix:
excluded keys therefore still need to exist there. The old 4999 key may require
multipart **copy**, but no new 4999 ceremony or key generation is involved.
Entries supplied by release assets stay unchanged and are omitted from S3 work.

Publish and verify every planned object before merging the staged source files
and deploying matching verifier code. Validate download hashes through the real
distribution endpoint and perform Zolana's deployment/integration checks. The
helper does not publish, copy, deploy, or generate a fresh local trusted setup.
Never run Zolana's all-key rotation script for this handoff: it would replace the
ceremony keys and excluded keys with new single-party setups.
