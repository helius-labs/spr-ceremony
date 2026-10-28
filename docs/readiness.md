# Core launch gates and validation

This repository implements the core ceremony workflow. The public-parameter
campaign is open; accepted contributions are recorded below. These implementation
checks do not replace independent security review or final-key verification.
Batch 4999 is deferred. Custom rings and batch 10 remain excluded.

## Implemented controls

- A shared production check pins the exact public Phase 1 SRS hash and size in
  preparation, opening, audit and export verification.
- Frozen compilation metadata and Phase 1 provenance, per-circuit transcripts,
  bounded contribution decoding, and cryptographic verification before acceptance.
  Independent audits reproduce each reduced SRS from the manifest's full Phase 1
  artifact before checking circuit initialization.
- Native preparation of the fixed public power-21 parameters, with resumed
  downloads, pinned input/output hashes, algebraic import, and explicit provenance
  distinguishing this path from historical transcript replay.
- Queued reservations, renewal, resumable downloads, idempotent submissions,
  bounded uploads, and disk preflight.
- An immutable checkpoint chain binding circuit heads and lifecycle. S3
  publication precedes acceptance; conditional writes reject stale coordinators.
- Production beacons pin quicknet’s chain hash. Explicit missed-deadline recovery
  preserves circuit states and records old/new rounds, reason and prior checkpoint.
- Manifest-bound coordinator OpenPGP signatures authenticate invitations and
  acceptance receipts in native clients, browsers and audits. Progress is keyed by
  unique invitation ID; duplicate active pseudonyms are rejected. Link reissue
  preserves ID and progress while revoking the previous token.
- Announced future drand beacon, close-before-beacon checks, frozen heads,
  signature verification, one final export, and independent key reproduction.
- Hash-verified S3 recovery including finalized keys; private tokens and leases
  are excluded. Terraform configures private encrypted/versioned storage.
- Zolana packaging without local setup and staged core-only release changes,
  preserving excluded pins. Linux/macOS binary builds include source metadata
  and checksums.

- Hosted CLI-only participant page with private invitation copying, a terminal
  installer, manual native downloads and public contribution records. The page
  never submits contributions or sends invitation credentials to the server;
  the native client authenticates invitations and receipts. Fixed checked assets
  and same-origin APIs support HTTPS hosting. [Hosting checks](hosted-ui.md).
- Separate operator control panel for invitation creation, revocation and progress;
  public access uses AWS IAM authentication and browser pairing. Peer/Host/Origin
  checks and strict JSON requests protect its local service boundary.
  Invitation tokens are absent from status, logs and participant workspaces.
- Native hidden-input `join`, pinned one-line installer, immutable GitHub releases
  and signed provenance bundles usable without repository access. Public artifact
  links derive from one checkpoint; final keys require finalized lifecycle state.
- AWS deployment code for HTTPS, isolated networking, SSM, persistent encrypted
  EBS, daily snapshots, private S3 and health/disk alarms. Deployment verifies
  release provenance and restores/audits the prepared campaign before serving.

## Validation performed

The automated Go suite covers plain and commitment-bearing fixtures, malformed
contributions, replay/stale submissions, export tampering, lifecycle/beacon
checks, reservation conflicts, checkpoint rewrites, and finalized-key recovery.
Tests run with the race detector. The toolchain is updated to Go 1.27.3 and
network dependencies to the patched versions. Gnark remains at v0.16.4;
gnark-crypto is pinned to v0.22.0 after compatibility validation.
Govulncheck 1.9.0 is required in CI; the current scan finds zero
called or imported-package vulnerabilities. Module-level advisories are reported
separately for code outside the imported package set; this is not a claim that
every package in every dependency module is free of advisories.
[The October 28 dependency check](evidence/security-check-2026-10-28.json) confirms
the deployed Go toolchain includes the eMudhra-related certificate fix. It records
the earlier gnark-crypto pin and three advisories outside the application's imports.
[The v0.22.0 compatibility report](evidence/gnark-crypto-v022/report.json) covers
all 41 live-manifest R1CS hashes, old key decoding, historical contributions,
and new contributions verified by the previous library. Fresh transfer and merge
proofs pass the Go and Solana verifiers. These checks do not alter the active
ceremony; release deployment is recorded separately below.
[Security update evidence](evidence/security-updates.json) records versions and
advisories. Recompiling all 41 circuits after these updates reproduced every
original R1CS hash. The S3 integration suite additionally exercises
presigned downloads, publication outages, lost PUT responses, stale ETags,
competing coordinators, corruption, and restore against Moto 5.1.14 pinned by
container digest. Moto is an emulator, not proof of AWS policy behavior.

Terraform formatting and configuration validation pass. The earlier internal SSM-only
demo ran in eu-north-1 without inbound ports or a public hostname.
Real AWS checks verified S3 encryption, versioning, CORS and conditional-write
policy; accepted contributions survived a stopped-coordinator S3 restore and
cryptographic audit. That two-circuit fixture was separate from the current 41-circuit public
campaign and was not a full failure/recovery certification. Rust verifier code is pinned to Zolana's actual
`groth16-solana` revision `70a93803e5f55b8425301d506c625169ef557c9d`.

The actual 41 core circuits compiled from Zolana
`ff04c625ceb43c688cd048549e00a5dabc0c6ee6`: 11,716,132 constraints in total,
maximum domain 2,097,152, and 1,366,876,096 bytes of compiled R1CS. All 41 passed
the real-witness rehearsal with two contributors, Zolana `.key` loading, proof
generation, and gnark verification. Current-source revalidation also passed all
41 Phase 1 derivation/checkpoint checks and pinned Solana host verifier checks,
including rejection of changed public inputs.

All 41 emitted Rust verifier modules passed the same valid/invalid proof checks.
Staging verified Go/TypeScript pins, 41 core uploads, and 83 unchanged-key copies;
every excluded pin was preserved. These are marked test fixtures assembled from
separate per-circuit campaigns, not a finalized production campaign or deployment.
The [report, public proof vectors, and logs](evidence/core-rehearsal/report.json)
record the revisions and artifact hashes. CI replays all 41 saved public proof
vectors against the pinned Rust verifier.

The resumed rehearsal runner used 24 parallel cases, took 86 minutes, and peaked
at about 52 GiB resident memory. It reused completed stages after an interruption;
these numbers are not a clean preparation benchmark or production capacity
certification. The report separates resumed timings from current-source checks.

The public power-21 Powers of Tau file matches the BLAKE2b published by snarkjs.
Full snarkjs 0.7.6 transcript verification, conversion, and SRS algebraic import
pass. A real Zolana transfer witness using these converted parameters passes
through two rehearsal contributors, key packaging, gnark verification, and the
pinned Solana verifier; changed public inputs are rejected.
[The provenance record and logs](evidence/public-phase1.json) preserve the evidence.
This run remains a rehearsal and is not an independent audit or production
multi-party campaign.

The native `phase1` command reproduced the exact verified PH1 and SRS without
external commands on `PATH`. Using an already downloaded transcript, it took
109.14 seconds and peaked at 3,333,256 KiB RSS on Linux with `GOMAXPROCS=4`.
This excludes download time and historical transcript replay. Corrupt inputs,
failed conversions/imports, cache tampering, concurrent preparation, and resumed
HTTP downloads are covered by fixture tests. macOS arm64 cross-compilation passes;
this is not a macOS execution benchmark. [Native import evidence](evidence/native-phase1.json)
records source hashes, artifact hashes, and logs.

The largest-constraint core circuit, `merge_52_1`, was measured separately with
`GOMAXPROCS=4`: about 40 seconds for local contribution plus verification, a
102.8 MB input, and approximately 1.19 GiB peak resident memory. This was one run
on a Ryzen 9 9950X with other rehearsal work active; it excludes transfers and is
not a minimum hardware guarantee. [Benchmark evidence](evidence/participant-benchmark.json)
includes the exact input size and process measurements.

A sequential benchmark of all 41 circuits on the same host took 377.6 seconds
(about 6 minutes 18 seconds) for local contribution and verification with
`GOMAXPROCS=4`, peaking at about 1.19 GiB resident memory. It used the marked
initial Phase 2 artifacts and changed no campaign state. This excludes transfers,
queue waits, and coordinator verification. The
[full participant benchmark](evidence/core-participant-benchmark.json) records
every circuit and the command used; it is not a guarantee for other hardware.

Across all 41 circuits, the second rehearsal contributor downloaded 979,814,019
bytes and uploaded the same amount, about 980 MB each way. These are serialized
artifact sizes, not measured network throughput; retries, metadata, and protocol
overhead are excluded. Prepared cryptographic artifacts total 3,145,703,323 bytes
after deduplicating shared parameters. Archive capacity must also cover accepted
contributions, final keys, temporary files, and retained object versions.
[Per-circuit payload evidence](evidence/core-payloads.json) records these counts.

The earlier [participant binary workflow](https://github.com/helius-labs/spr-ceremony/actions/runs/40566501764)
built all four Linux/macOS targets and signed their provenance. Downloaded checksums
and the GitHub attestation were verified. All four binaries were reproduced byte
for byte in the pinned Go container with the same module-cache path as CI. The
Linux amd64 binary was executed and
scanned; the other architectures were cross-compiled but not executed on native
hardware. [Artifact evidence](evidence/binary-provenance.json) records the digests.

The immutable [v0.1.0-rc.2 release](https://github.com/helius-labs/spr-ceremony/releases/tag/v0.1.0-rc.2)
was built from `14104185f9f6e5272c1702ecb5f8c4a7c6875b09`. Its release pipeline
verified all eight build attestations and executed contribution, export and
hidden-input/cancellation checks on native Linux and macOS ARM64/x86-64 runners.
Downloaded assets matched the immutable release attestation. The published
HTTPS installer completed two real contributions to marked test circuits with
hidden invitation input and no invitation token in captured output or logs.
This used a local HTTPS server and test CA, not an AWS deployment or a production
campaign. [Release evidence](evidence/release-v0.1.0-rc.2.json) records hashes,
workflow results and limits; this does not certify full-core performance on
every platform or execution in macOS Safari.

A live quicknet response was verified against the chain hash published by drand,
including an exact-round fetch from a second endpoint and rejection of altered
signatures, rounds, and randomness. The saved [public beacon fixture](evidence/live-beacon.json)
also runs offline in CI. It was not used to finalize any campaign.

The signed core controls are released in
[v0.1.0-rc.5](https://github.com/helius-labs/spr-ceremony/releases/tag/v0.1.0-rc.5)
and were deployed on the internal AWS instance. Chromium, WebKit and the native client
completed the two-circuit campaign through SSM and real S3 downloads. The AWS
restore drill preserved accepted work, restored the signing key from Secrets
Manager after its local copy was removed, and verified explicit ID reissue.
GnuPG independently verified an actual hosted invitation and acceptance receipt,
and rejected altered payloads. [Deployment evidence](evidence/aws-internal.json)
records the release, checks, public identity and remaining limits.

## Release gates

Items 1–4 apply before opening the public campaign. Item 5 applies before using
its keys in Zolana.

1. Independent review of the MPC engine/version, Phase 1 bridge and provenance,
   contribution decoding, final beacon construction, checkpoint model, and
   Zolana key conversion. Resolve findings and freeze the reviewed source/binaries.
2. Test the full native core session on intended Mac/Linux hardware, including
   laptop suspension, disk pressure and recovery. Check the invitation website
   in intended browsers; Linux Playwright WebKit is not Safari. Measure
   coordinator/participant RAM, disk, runtimes, and network throughput
   on intended hardware. The all-41 compatibility rehearsal above passes; server
   limits and resumed test-run measurements are not hardware certification.
3. A real AWS staging drill with the deployed IAM/bucket policy: presigned access,
   interrupted transfer, response loss/retry, coordinator crash, restore/audit,
   competing coordinator rejection, and final-key recovery. Confirm encryption,
   version retention, alarm routing, fencing, and backup ownership. The internal two-circuit deployment has passed initial policy and restore
   checks; full-core failure, final-key recovery and alert delivery checks remain.
4. Freeze and independently reproduce the chosen public Phase 1 transcript and
   circuit compilation. Announce and externally witness the manifest, beacon
   network/round, and closure checkpoint before the beacon is revealed.
5. Run the actual multi-party campaign, with agreed participation requirements
   per circuit. A synthetic rehearsal never supplies production keys. Independently
   reproduce the final keys, test the complete staged Zolana release/deployment,
   publish every planned distribution object, and only then update consumers.

## Operational limits

One active coordinator; no automatic failover. S3 is the archive/commit boundary,
not a distributed scheduler. Local locking serializes mutations and publication.
Invitations are issued administratively. Hosted requests have bounded concurrency
and per-peer burst limits; distributed network attack protection is separate.
Forwarded addresses are trusted only from an explicitly configured loopback proxy.
The built-in health endpoint does not probe S3; the AWS health timer also checks
bucket access. JSON metadata reads are capped at
4 MiB (including an individual circuit history); plan a bounded campaign and
measure metadata growth before expanding to thousands of participants.

Cryptographic computation loads vectors into RAM and is not resumable mid-step.
Preparation publishes the complete campaign atomically; an interrupted preparation
run must be repeated. S3 recovery begins once preparation has completed and been
synced. Keep the preparation host and its persistent storage available throughout.
S3 artifacts use single PUTs up to 5 GiB. There is no transcript garbage collector,
account-wide deletion protection, attested participant host, or automatic secret
erasure guarantee. Checkpoint timestamps require external witnessing; signatures
and hashes alone cannot prove when the operator announced or closed a ceremony.

## Current public-parameter campaign

The operator opened the 41-circuit campaign with manifest
`4b2b4465b79b5670d896e18dfb6fb86d02023033e9aeeb43e7a14911cb402079`.
It uses the pinned public power-21 SRS. Its original signed metadata contains a
legacy test marker; the source parameter digest determines which security checks
apply. Public parameters require quicknet, coordinator signatures, complete core
scope and planned-beacon finalization even when this marker is present. Signed
manifests, invitations and receipts retain their original bytes. New final exports
classify parameter provenance consistently with independent export verification.

The initialization audit passed all 41 circuits on the original prepared snapshot.
The first two contributors' full transcript audit also passed all 41 circuits.
Later contributions, final-key verification and independent review remain
separately verifiable requirements. The current display identifies the
actual public parameters instead of claiming that they were generated locally.

The previous AWS deployment ran
[v0.1.0-rc.9](https://github.com/helius-labs/spr-ceremony/releases/tag/v0.1.0-rc.9),
source `e626dd0a888d8bdb4d74ae4f20398afcacd1c8bb`. Native release checks passed
on Linux and macOS, ARM64 and x86-64. The first contributor has 41/41 accepted
receipts at checkpoint
`1214e784afb0250bfc2b72ba3348a805cd1e4e74253d79177c9e80bb4a3f9f1d`.
Deployment preserved the manifest, checkpoint, invitations and planned beacon.
Chromium and WebKit checks against the live HTTPS site confirm that public
parameters display without local-demo warnings. The CLI reports `[n/41]`
progress. Operator binaries must be rc.9 or newer: earlier clients reject the added
status fields. Acceptance is separate from completion of the independent full
transcript audit. [Deployment evidence](evidence/release-v0.1.0-rc.9.json).

The [initial audit](evidence/core-initial-audit.json) passed on the enlarged AWS
instance with 24 circuit workers; its largest job took about 72 minutes. The
[full log](evidence/core-initial-audit.log) covers every initial circuit. The
rc.9 audit separately passed the checkpoint containing the first contributor's
41 accepted transitions. The [result](evidence/core-first-contributor-audit.json)
and [full log](evidence/core-first-contributor-audit.log) bind that result to the
snapshot and executable. They do not cover subsequently accepted contributions.

A [full-core S3 restore](evidence/core-s3-restore.json) recovered 257 objects
(4,125,901,577 bytes), the checkpoint history and all 41 receipts in 171 seconds,
including a second hash pass over every restored object. It used an isolated
directory and preserved the live campaign. No invitation tokens, private key,
leases or S3 configuration were restored. This verifies archive recovery, not
live failover or finalized-key recovery.

[GnuPG independently verified all 123 acceptance receipts](evidence/core-receipt-signatures.json)
for the first three contributors against the manifest-bound public key and rejected
all 123 altered payloads.
This validates coordinator receipt authentication, separately from the completed
cryptographic transcript audit.

[v0.1.0-rc.10](evidence/release-v0.1.0-rc.10.json) introduced automatic
completed-contributor records. The public endpoint and GitHub mirror derive
records from pinned checkpoint states and verify coordinator signatures. The
first Actions run archived all 41 receipts; a second run produced no duplicate
commit. Scheduled publication is enabled. The live Chromium/WebKit checks show
the completed first contributor and exclude the partial second contributor.
Deployment followed the operator-confirmed pause and preserved both contributors'
accepted work. GitHub publication is independent of S3 acceptance and labels
records as accepted, without claiming completion of the full MPC audit.

The second contributor completed all 41 circuits at checkpoint
`03f11b8310922a9e66d62462287bbc363dfeec4bcb70399d385079d8061a15d1`.
Scheduled [publication run 40728227669](https://github.com/helius-labs/spr-ceremony/actions/runs/40728227669)
verified and archived that completed record in commit `77fed95`, preserving the
first contributor's existing record. Both completed records are visible through
the public endpoint. The full audit above remains scoped to the earlier first
contributor checkpoint.

[v0.1.0-rc.11](evidence/release-v0.1.0-rc.11.json) was deployed from
`29249c8f989091163d7dbdeeef02d921945891e1`, with Go 1.27.3 and gnark-crypto
v0.22.0. All four native release checks passed. The public installer and all
four hosted binaries match the attested release; live Chromium and WebKit checks
show all three completed contributors. Deployment waited for Tilo to finish and
preserved the full operator status at checkpoint
`e4f2c3a01a28f149c40680bc50b42b6eff5d57acca3486aa82c69993183497ea`,
including all 123 accepted contributions and the original beacon plan.
Publication run `40737106873` archived Tilo's record in commit `99b6470`,
preserving the earlier records. Its first attempt overlapped the coordinator
restart; the retry passed after deployment.

Hosted finalization is configured for 24 circuit workers, 96 Go CPU slots and a
96 GiB memory limit. Finalization and independent export verification can rebuild
circuits in parallel while preserving deterministic output order. The live core
campaign has not been finalized; no full-core finalization runtime is claimed.
The first-two-contributor audit continued across deployment on its original
snapshot and binary. It does not cover Tilo's later contributions.

That [two-contributor audit](evidence/core-two-contributor-audit.json) has now
passed all 41 initializations and 82 contribution transitions. Its
[full log](evidence/core-two-contributor-audit.log) records each circuit. Later
contributions remain outside that pinned checkpoint.

The [October 28 AWS operations check](evidence/aws-operations-2026-10-28.json)
confirmed private versioned S3 storage, encrypted EBS, healthy disk/service alarms,
an enabled daily snapshot policy and a current signing-key backup version in
Secrets Manager. No private key value was read. The first private encrypted
snapshot completed and passed an [isolated disk restore](evidence/core-ebs-restore.json):
509 referenced public objects (6,091,537,809 bytes), 127 checkpoints and 125
signed receipts were verified. The restored checkpoint was authenticated against
the operator-pinned live history. The private signing-key file was present with
mode 0600; its contents were not read. The temporary restored volume was unmounted
and deleted, retaining the original disk and backup snapshot. This was not a live
failover or final-key recovery drill.

No confirmed alarm recipient is configured, so alert delivery remains unverified.
Use `scripts/check-aws-operations.py` for current configuration checks; it exits
nonzero while that configuration gap remains and does not send notifications.

[v0.1.0-rc.12](evidence/release-v0.1.0-rc.12.json) is deployed from
`454bf6bb8184be32cd6ba8f85af264817d0095e4`. The participant page is CLI-only:
it shows the terminal command, private invitation copying and native downloads.
Browser contribution controls and invitation-file upload are removed. The page
does not load the contribution engine, use browser storage or send invitation
credentials to the coordinator. Native clients perform invitation validation.

CI and all four native release checks passed. Live Chromium/WebKit checks and
public binary/installer hashes match the release. Deployment preserved the full
operator status, including 140 accepted circuit contributions: 41 each from
Sergei, Alex and Tilo, and 17 from Jorrit. The manifest and beacon plan are unchanged.

[v0.1.0-rc.13](evidence/release-v0.1.0-rc.13.json) is deployed from
`de4291f9451f99c0ab90522ed004209b2938f213`. Public `audit --url` accepts
independently authenticated manifest and checkpoint hashes without AWS credentials
or invitations. It shares the full transcript verifier and reusable download
workspace with operator audits. Historical checkpoint pins remain fixed while
the live campaign advances; altered metadata, mixed ceremony pins and corrupt
cached objects are rejected.

CI, including S3 integration, and native CLI checks on all four platforms passed.
The hosted installer and binaries match the attested release; live Chromium and
WebKit checks passed. The full operator status, including all 140 accepted
contributions, remained unchanged. The new command was exercised against small
signed fixtures; no additional full-core audit or finalization was performed.

A subsequent [public audit recovery check](evidence/public-audit-recovery.json)
interrupted an isolated AWS audit during snapshot download, then resumed the same
pinned checkpoint with rc.13. All 342 cached objects (6,186,217,858 bytes) retained
their files and passed another SHA256 check. The full live operator status was
unchanged. No partial object transfer was present at interruption, so this checks
workspace recovery rather than a mid-object Range resume. The resumed full-core
audit subsequently [passed](evidence/core-public-audit.json).

That check exposed repeated hashing of shared artifacts while restoring historical
checkpoints. Restore now verifies each unique object on arrival and checks the
complete staged set again before publication. A [saved-checkpoint comparison](evidence/restore-performance.json)
on AWS restored the same 380 objects (5.1 GB) in 7.7 seconds instead of 281 seconds;
reads fell from 325 GB to 10.2 GB. Both outputs matched and the temporary restored
directories were removed. This used warm cached files and local hard links,
excludes download time, and does not measure cryptographic audit performance.
Tests reject corrupt downloads, objects altered after their first check,
conflicting size metadata and cancellation without publishing a failed restore.

[v0.1.0-rc.14](evidence/release-v0.1.0-rc.14.json) deploys that restore improvement
from `28ec63125ac318a1390eddedd632469dead3281e`. CI, S3 integration and all four
native platform checks passed. The public installer and binaries match the
attested release, and live Chromium/WebKit checks passed. Deployment preserved
the complete operator status, all 140 accepted contributions and the beacon plan.
The separate full-core audit continued on its immutable rc.13 executable without
restarting; it subsequently [passed](evidence/core-public-audit.json).

A [production signing-key recovery check](evidence/signing-key-recovery.json)
retrieved the manifest-specific Secrets Manager backup into a private temporary
directory on the AWS host's memory filesystem. The released CLI matched it to
the public transcript key; GnuPG signed a distinct recovery challenge, verified
it against that public key and rejected altered content. Temporary keys and
GnuPG homes were removed. No private material was printed, returned from AWS or
placed in the repository. The live key and full operator status were unchanged.
This verifies recovery of the coordinator identity, separately from transcript
verification and without asserting secure memory erasure.

The [full public transcript audit](evidence/core-public-audit.json) passed all 41
core circuits and 140 contribution transitions at checkpoint
`8ce40de71626f9754b8019f1980be5afe9c416b00aa5d887ed7ecebe79fefe23`.
It downloaded public artifacts without AWS credentials or invitations and used
24 verification workers. The collector additionally checked the SHA256 hashes
of all 554 restored objects (6,187,060,783 bytes). The [complete log](evidence/core-public-audit.log)
and [collector source](evidence/core-public-audit-collector.py.txt) are retained.
This result covers the pinned snapshot only; subsequent contributions, final
keys and independent security review remain separate. The ceremony stayed open.

[v0.1.0-rc.15](evidence/release-v0.1.0-rc.15.json) adds optional post-contribution
GitHub account links, separately signed and archived without changing MPC
checkpoints or receipts. Native and hosted tests cover explicit consent,
session/replay checks, immutable account binding and sidecar recovery; OAuth is
simulated in these tests. The record-publication workflow passed against the
live public records. Upload capacity responses now wait without consuming the
four transient network retry attempts, bounded by cancellation, invitation expiry
and a 30-minute upload deadline. Ten concurrent clients completed and audited
two fixture circuits each. All four native release platform checks passed.
This release has not been deployed; real OAuth app setup and live linking remain
pending. See [GitHub setup](github.md).
