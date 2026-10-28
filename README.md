# Solana Privacy Rings

A trusted setup for **41 core circuits**. Signed invitations and receipts.
Verified contributions. Public final keys.

[Participant site](https://d3gz3drevfa8mb.cloudfront.net) ·
[Control panel](https://1q82ow4q4c.execute-api.eu-north-1.amazonaws.com) ·
[CLI releases](https://github.com/helius-labs/spr-ceremony/releases) ·
[Contribution records](contributions/)

## Contribute

Get a private invitation link from the operator. Run this in your terminal:

```sh
curl --proto '=https' --tlsv1.2 -fsS https://d3gz3drevfa8mb.cloudfront.net/join.sh | SPR_CEREMONY_DOWNLOAD_BASE=https://d3gz3drevfa8mb.cloudfront.net/native bash
```

Paste your invitation link at the hidden prompt. The command downloads the
matching Mac/Linux binary, checks its pinned SHA256 and contributes to all 41
circuits. **No compilation, Go, AWS account, GitHub access or sudo.** Requires
Bash, curl and a SHA256 utility, included on macOS and common Linux installations.

Keep the computer awake. Progress shows `[1/41]` through `[41/41]`. Rerun the
same command after interruption; accepted work is skipped. Public work and
receipts stay in `~/.local/share/spr-ceremony/work`.

Open your invitation link to copy the command and your private invitation.
Participation runs in the terminal. With an existing binary, run
`./spr-ceremony join` and paste the link at its prompt. If you received an
invitation file instead:

```sh
./spr-ceremony contribute --invitation bob.json
```

The one-liner trusts the hosted installer. For independent release provenance
verification, follow the [verification guide](docs/releases.md).

## Operate

Download the latest release binary for your platform, rename it `spr-ceremony`,
and run `chmod +x spr-ceremony`. Operators also need the AWS CLI and their
configured SSO profile. Connect once:

```sh
aws sso login --profile YOUR_PROFILE
./spr-ceremony operator connect --operator-url https://1q82ow4q4c.execute-api.eu-north-1.amazonaws.com --profile YOUR_PROFILE
```

**Create an invitation link:** open the control panel, click **Sign in**, and run
its displayed `./spr-ceremony operator login CODE` command. Enter a contributor
name, click **Create invitation**, then **Copy link**. Send the link and the
contributor one-liner privately. No SSM tunnel is needed.

**Create an invitation file from the CLI:**

```sh
./spr-ceremony operator invite bob --out invitations/bob.json
```

Send the file and the printed contributor command privately. This command saves
a file; it does not print a secret link. Invitations expire after 24 hours by
default; use `--hours 72` to change that.

**Check or manage contributions:**

```sh
./spr-ceremony operator status
./spr-ceremony operator audit --workers 4
./spr-ceremony operator reissue bob
./spr-ceremony operator revoke bob
```

Audit downloads and verifies the full transcript locally; parallel workers need
additional RAM. Reissue preserves accepted work and invalidates previous access.
Revoke also preserves accepted work. The current ceremony is already open.

**Finish:** close before the announced beacon time, finalize after it, then
independently reproduce the published keys:

```sh
./spr-ceremony operator close
# After the beacon time shown by operator status:
./spr-ceremony operator finalize
./spr-ceremony operator export --out final-keys
```

[Operator guide](docs/operator-cli.md) · [Preparation and recovery](docs/operations.md) ·
[AWS deployment](docs/aws.md) · [Validation status](docs/readiness.md)

Completed contributions appear automatically on the participant site. A separate
GitHub workflow mirrors verified receipt records; acceptance never waits for
GitHub. [Publication details](docs/operator-cli.md#contribution-records).

## Build from source

Optional; requires Go 1.27.3:

```sh
go build -o spr-ceremony ./cmd/spr-ceremony
```

[Local CLI and website demo](docs/hosted-ui.md). Custom rings, batch 10 and batch 4999 are
excluded. The engine is pinned to gnark `v0.16.4` and gnark-crypto
`v0.22.0`.
