# Core launch gates and validation

This repository implements the core ceremony workflow. It is not an independent
security audit or evidence that a public ceremony has already taken place.
Batch 4999 is deferred. Custom rings and batch 10 remain excluded.

## Implemented controls

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
- Announced future drand beacon, close-before-beacon checks, frozen heads,
  signature verification, one final export, and independent key reproduction.
- Hash-verified S3 recovery including finalized keys; private tokens and leases
  are excluded. Terraform configures private encrypted/versioned storage.
- Zolana packaging without local setup and staged core-only release changes,
  preserving excluded pins. Linux/macOS binary builds include source metadata
  and checksums.

- Hosted core UI with private invitations, WebAssembly workers, renewable leases,
  public download/output recovery, checked receipts, and native-client handoff.
  Fixed checked assets, same-origin APIs, bounded downloads, and optional S3 CORS
  support dedicated HTTPS hosting. [Hosting and browser checks](hosted-ui.md).
- Separate SSM control panel for invitation creation, revocation and progress;
  peer/Host/Origin checks and strict JSON requests protect its loopback boundary.
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
network dependencies to the patched versions; gnark and gnark-crypto pins remain
unchanged. Govulncheck 1.9.0 is required in CI; the current scan finds zero
called or imported-package vulnerabilities. Module-level advisories are reported
separately for code outside the imported package set; this is not a claim that
every package in every dependency module is free of advisories.
[Security update evidence](evidence/security-updates.json) records versions and
advisories. Recompiling all 41 circuits after these updates reproduced every
original R1CS hash. The S3 integration suite additionally exercises
presigned downloads, publication outages, lost PUT responses, stale ETags,
competing coordinators, corruption, and restore against Moto 5.1.14 pinned by
container digest. Moto is an emulator, not proof of AWS policy behavior.

Terraform formatting and configuration validation pass; no production account
resources have been applied. A read-only plan in eu-north-1 creates 31 isolated
resources and modifies none; hostname, budget and live recovery validation remain
outstanding. Rust verifier code is pinned to Zolana's actual
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

The [participant binary workflow](https://github.com/helius-labs/spr-ceremony/actions/runs/40566501764)
built all four Linux/macOS targets and signed their provenance. Downloaded checksums
and the GitHub attestation were verified. All four binaries were reproduced byte
for byte in the pinned Go container with the same module-cache path as CI. The
Linux amd64 binary was executed and
scanned; the other architectures were cross-compiled but not executed on native
hardware. [Artifact evidence](evidence/binary-provenance.json) records the digests.

A live quicknet response was verified against the chain hash published by drand,
including an exact-round fetch from a second endpoint and rejection of altered
signatures, rounds, and randomness. The saved [public beacon fixture](evidence/live-beacon.json)
also runs offline in CI. It was not used to finalize any campaign.

## Release gates

Items 1–4 apply before opening the public campaign. Item 5 applies before using
its keys in Zolana.

1. Independent review of the MPC engine/version, Phase 1 bridge and provenance,
   contribution decoding, final beacon construction, checkpoint model, and
   Zolana key conversion. Resolve findings and freeze the reviewed source/binaries.
2. Test the full hosted core session on intended browsers, including macOS Safari,
   laptop suspension/reopening, storage pressure, and recovery. Linux Playwright
   WebKit is not Safari. Measure coordinator/participant RAM, disk, runtimes, and network throughput
   on intended hardware. The all-41 compatibility rehearsal above passes; server
   limits and resumed test-run measurements are not hardware certification.
3. A real AWS staging drill with the deployed IAM/bucket policy: presigned access,
   interrupted transfer, response loss/retry, coordinator crash, restore/audit,
   competing coordinator rejection, and final-key recovery. Confirm encryption,
   version retention, alarm routing, fencing, and backup ownership. No actual
   production resources have been deployed yet; eu-north-1 access and the
   proposed Terraform plan have been checked.
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
