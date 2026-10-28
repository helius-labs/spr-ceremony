# Releases and terminal participation

The participant page provides this command with its real hostname:

```sh
curl --proto '=https' --tlsv1.2 -fsS https://CEREMONY_HOST/join.sh | SPR_CEREMONY_DOWNLOAD_BASE=https://CEREMONY_HOST/native bash
```

The invitation is entered at a hidden prompt, never included in the command,
process arguments or shell history. The installer supports macOS and Linux on
ARM64 and x86-64. It downloads to a private temporary directory, checks the
binary against an embedded SHA256, runs it, then removes the executable.
Public work remains in `~/.local/share/spr-ceremony/work`; set
`SPR_CEREMONY_WORK` to choose another directory. No private invitation is saved.

A checksum delivered by the same website does not independently authenticate
that website. The one-liner trusts the host's installer. For stronger assurance,
download the binary and `provenance.json`, then verify before execution:

```sh
gh attestation verify spr-ceremony-darwin-arm64 \
  --repo helius-labs/spr-ceremony --bundle provenance.json \
  --source-digest REVIEWED_SOURCE_COMMIT \
  --signer-workflow helius-labs/spr-ceremony/.github/workflows/binaries.yml \
  --deny-self-hosted-runners
chmod +x spr-ceremony-darwin-arm64
./spr-ceremony-darwin-arm64 join
```

Use the filename for your platform. Authenticate the source commit separately.
The bundle allows verification without access to this internal repository;
GitHub's trusted signing roots are fetched independently. For fully offline
verification, obtain trusted roots independently as described in
[GitHub's verification guide](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/verify-attestations-offline).
The expected repository ID is `1435851983`; this survives repository renames.

## Publish

The **Release** workflow accepts `vMAJOR.MINOR.PATCH` or `vMAJOR.MINOR.PATCH-rc.N`.
It requires a successful Check run on the exact main commit, builds all four
binaries and browser assets, signs their provenance, verifies every asset,
runs native contribution/export/terminal checks on all four target platforms,
uploads a draft, then publishes an immutable release. Existing versions are not
overwritten. Keep release immutability enabled in repository settings.

Automatic contribution-record commits do not trigger push checks. If the current
main commit has no successful Check run, start one manually and wait for it to
pass before releasing. Release accepts push or manual checks on that exact main
commit; pull-request checks do not satisfy the gate.

```sh
gh workflow run check.yml --ref main
```

```sh
gh workflow run binaries.yml --ref main -f version=NEW_VERSION
```

GitHub downloads require repository access. The AWS deployment helper verifies
the release and mirrors the same bytes at `/native/` and `/join.sh`. It never
places a GitHub credential on the server. Release binaries contain no ceremony
secrets or final keys. Final keys belong to an independently finalized campaign.
