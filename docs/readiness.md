# Core launch gates and validation

This repository implements the core ceremony workflow. It is not an independent
security audit or evidence that a public ceremony has already taken place.
Batch 4999 is deferred. Custom rings and batch 10 remain excluded.

## Implemented controls

- Frozen compilation metadata and Phase 1 provenance, per-circuit transcripts,
  bounded contribution decoding, and cryptographic verification before acceptance.
  Independent audits reproduce each reduced SRS from the manifest's full Phase 1
  artifact before checking circuit initialization.
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

## Validation performed

The automated Go suite covers plain and commitment-bearing fixtures, malformed
contributions, replay/stale submissions, export tampering, lifecycle/beacon
checks, reservation conflicts, checkpoint rewrites, and finalized-key recovery.
Tests run with the race detector. The toolchain is updated to Go 1.27.3 and
network dependencies to the patched versions; gnark and gnark-crypto pins remain
unchanged. Govulncheck 1.9.0 is required in CI; the current scan finds zero
called or imported-package vulnerabilities. The remaining module advisory is for
`x/crypto/openpgp`, which is not imported.
[Security update evidence](evidence/security-updates.json) records versions and
advisories. Recompiling all 41 circuits after these updates reproduced every
original R1CS hash. The S3 integration suite additionally exercises
presigned downloads, publication outages, lost PUT responses, stale ETags,
competing coordinators, corruption, and restore against Moto 5.1.14 pinned by
container digest. Moto is an emulator, not proof of AWS policy behavior.

Terraform formatting and configuration validation pass; no production account
resources have been applied. Rust verifier code is pinned to Zolana's actual
`groth16-solana` revision `70a93803e5f55b8425301d506c625169ef557c9d`.

The actual 41 core circuits compiled from Zolana
`ff04c625ceb43c688cd048549e00a5dabc0c6ee6`: 11,716,132 constraints in total,
maximum domain 2,097,152, and 1,366,876,096 bytes of compiled R1CS. A complete
real-witness ceremony rehearsal is in progress; final results must be recorded
before treating the all-circuit integration gate as satisfied.

The public power-21 Powers of Tau file matches the BLAKE2b published by snarkjs.
Full snarkjs 0.7.6 transcript verification, conversion, and SRS algebraic import
pass. A real Zolana transfer witness using these converted parameters passes
through two rehearsal contributors, key packaging, gnark verification, and the
pinned Solana verifier; changed public inputs are rejected.
[The provenance record and logs](evidence/public-phase1.json) preserve the evidence.
This run remains a rehearsal and is not an independent audit or production
multi-party campaign.

The largest-constraint core circuit, `merge_52_1`, was measured separately with
`GOMAXPROCS=4`: about 40 seconds for local contribution plus verification, a
102.8 MB input, and approximately 1.19 GiB peak resident memory. This was one run
on a Ryzen 9 9950X with other rehearsal work active; it excludes transfers and is
not a minimum hardware guarantee. [Benchmark evidence](evidence/participant-benchmark.json)
includes the exact input size and process measurements.

The [participant binary workflow](https://github.com/helius-labs/spr-ceremony/actions/runs/40548909358)
built all four Linux/macOS targets and signed their provenance. Downloaded checksums
and the GitHub attestation were verified. The Linux amd64 binary was executed and
scanned; the other architectures were cross-compiled but not executed on native
hardware. [Artifact evidence](evidence/binary-provenance.json) records the digests.

## Release gates

Items 1–4 apply before opening the public campaign. Item 5 applies before using
its keys in Zolana.

1. Independent review of the MPC engine/version, Phase 1 bridge and provenance,
   contribution decoding, final beacon construction, checkpoint model, and
   Zolana key conversion. Resolve findings and freeze the reviewed source/binaries.
2. Complete all-41 real-circuit rehearsal, including Zolana `.key` loading,
   valid proofs and invalid-public-input rejection in the pinned Solana verifier.
   Record actual coordinator/participant RAM, disk, runtimes, and bandwidth on
   intended hardware. Server limits are controls, not hardware certification.
3. A real AWS staging drill with the deployed IAM/bucket policy: presigned access,
   interrupted transfer, response loss/retry, coordinator crash, restore/audit,
   competing coordinator rejection, and final-key recovery. Confirm encryption,
   version retention, alarm routing, fencing, and backup ownership. No actual
   bucket/region has been selected or deployed in this work yet.
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
Invitations are issued administratively; public endpoints need proxy rate limits.
The built-in health endpoint does not probe S3. JSON metadata reads are capped at
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
