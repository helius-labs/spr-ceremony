# Final core keys

Finalized November 2, 2026: **41 circuits, 15 contributions per circuit, 615 total**.
AWS finalization completed at approximately 17:34 UTC. Independent reproduction
of all proving and verifying keys completed at 18:52 UTC using CLI `v0.1.0-rc.18`.

[Download the keys and evidence](https://github.com/helius-labs/spr-ceremony/releases/tag/core-keys-2026-11-02) ·
[Public ceremony](https://d3gz3drevfa8mb.cloudfront.net) ·
[Verification report](evidence/core-finalization-2026-11-02/verification.json)

## Download

The release contains 41 `.pk` proving keys, 41 `.vk` verifying keys, `export.json`
and `keys.SHA256SUMS` (approximately 2.34 GB in total). These are gnark key files;
the application `.key` packaging is a separate step.

With the GitHub CLI:

```sh
gh release download core-keys-2026-11-02 --repo helius-labs/spr-ceremony \
  --pattern '*.pk' --pattern '*.vk' --pattern export.json \
  --pattern keys.SHA256SUMS --dir final-keys
(cd final-keys && shasum -a 256 -c keys.SHA256SUMS)
```

Hash checks detect changed downloads; they do not replay the ceremony. Use a
recent GitHub CLI's `gh release verify core-keys-2026-11-02 --repo helius-labs/spr-ceremony`
to verify GitHub's release attestation. Review the authenticated pins below.

An already connected operator can download and independently reproduce the keys:

```sh
./spr-ceremony operator export --out final-keys --workers 4
```

## Frozen pins

| Artifact | SHA256 |
| --- | --- |
| Manifest | `4b2b4465b79b5670d896e18dfb6fb86d02023033e9aeeb43e7a14911cb402079` |
| Finalized checkpoint | `5c525e4b1547516cf63678c4a235cb2fd05cd1f78a463a3faf44e2ef1cbc41d7` |
| Export metadata | `ed506cb30158a722fcc9692d44960599e73a4028828b0e6dc6266ec597af8caa` |
| Closure checkpoint | `6ddc392b09d1a93b17bce65c08731d5b11616b851986454a5efe2a1554f7f61b` |

Circuit source: `ff04c625ceb43c688cd048549e00a5dabc0c6ee6`.
Quicknet round **33610948**, revealed at **16:16:48 UTC**. The
[closure release](https://github.com/helius-labs/spr-ceremony/releases/tag/core-closure-2026-11-02)
was independently timestamped at **14:47:17 UTC**, before that beacon.
The export includes the beacon signature and randomness; the verifier checks
them against the pinned quicknet chain and the frozen circuit heads.

## Independently verify without AWS access

After downloading the release keys, audit the public transcript, then reproduce
the keys against that exact checkpoint:

```sh
manifest=4b2b4465b79b5670d896e18dfb6fb86d02023033e9aeeb43e7a14911cb402079
checkpoint=5c525e4b1547516cf63678c4a235cb2fd05cd1f78a463a3faf44e2ef1cbc41d7
./spr-ceremony audit --url https://d3gz3drevfa8mb.cloudfront.net \
  --manifest-sha256 "$manifest" --checkpoint-sha256 "$checkpoint" \
  --work audit --workers 4
./spr-ceremony verify-export --dir "audit/$manifest/$checkpoint" \
  --export final-keys --manifest-sha256 "$manifest" \
  --checkpoint-sha256 "$checkpoint" --workers 4
```

Both commands perform cryptographic work and can take hours. Keep `audit/` to
reuse downloads. More workers need more RAM. Our independent final-key check
took approximately 78 minutes with 12 workers and `GOMAXPROCS=24`; timings depend
on hardware. Logs and exact artifact hashes are in the verification report.

## Integrate into Solana Privacy Rings

The ceremony is complete. Application integration is next:

1. Package the final `.pk`/`.vk` and pinned R1CS into the prover's `.key` format.
2. Generate the matching Rust verifier constants and Go/TypeScript key hashes.
3. Generate real proofs with every final key; verify them in gnark and the Solana
   verifier, including rejection of changed public inputs.
4. Publish the application key bundle, then release the matching prover, SDK
   pins and on-chain verifier together.

Use the existing [packaging and staging commands](operations.md#stage-the-zolana-release).
These final-key application proof tests and deployments have not yet been run.
Do not reinitialize the ceremony or generate another local trusted setup.
Custom rings, batch 10 and batch 4999 remain outside this ceremony.

The full transcript and final keys also remain archived under
`s3://spr-ceremony-test-558215002830-artifacts/core/v1/`.
The historical bucket name does not change the setup parameters or key provenance.
