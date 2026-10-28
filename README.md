# Solana Privacy Rings

A trusted setup for **41 core circuits**. Browser or terminal. Private invitations.
Verified contributions. Signed invitations and receipts. Public final keys.

Download the latest [release candidate](https://github.com/helius-labs/spr-ceremony/releases).
Operate and contribute with the CLI; no SSM tunnel is needed.

Custom rings, batch 10 and batch 4999 are excluded. The cryptographic engine is
pinned to gnark `v0.16.4` and gnark-crypto `v0.21.1-0.20260913224755-1409fe592052`.

## Contribute

Open your private invitation and click **Contribute**. Keep the tab open.
For faster contributions, choose **Use terminal** and copy the one-line command.
It downloads the matching Mac/Linux binary, checks its pinned SHA256, then asks
for your invitation with input hidden. No Go, npm, AWS account or sudo required.

Already have the [release binary](https://github.com/helius-labs/spr-ceremony/releases)?

```sh
./spr-ceremony join
```

Rerun after interruption. Accepted work is skipped; completed public output is
reused while its parent is current. Save your receipts. Share invitations privately.

The hosted code is part of your trust boundary. For independent verification,
check the release's signed provenance and reviewed source commit before running
it. [Verification instructions](docs/releases.md).

## Run a rehearsal

Source builds need Go 1.27.3. The browser build needs Go and a shell.

```sh
go build -o spr-ceremony ./cmd/spr-ceremony
bash scripts/build-browser.sh
./spr-ceremony demo --out rehearsal
./spr-ceremony serve --dir rehearsal --web-dir dist/browser --public-url http://127.0.0.1:8080
```

In another terminal, start the private control panel:

```sh
./spr-ceremony admin --dir rehearsal --public-url http://127.0.0.1:8080
```

Open **http://127.0.0.1:8081**, enter a pseudonym, and create an invitation.
Use **Reissue link** to renew access and preserve progress.
Rehearsal proving keys are explicitly insecure. [Testing guide](docs/hosted-ui.md).

## Operate

Use your AWS SSO profile; no SSM tunnel is needed:

```sh
aws sso login --profile YOUR_PROFILE
./spr-ceremony operator connect --operator-url OPERATOR_URL --profile YOUR_PROFILE
./spr-ceremony operator open --hours 24
./spr-ceremony operator invite bob
./spr-ceremony operator status
./spr-ceremony operator audit
```

`invite` saves a private invitation file and prints the contributor's command.
Send both privately. `audit` downloads and verifies the current public transcript.
[CLI guide](docs/operator-cli.md) · [Mac-to-AWS ceremony guide](docs/rehearsal.md).

[Deploy to AWS and invite contributors](docs/aws.md). Terraform provisions HTTPS,
private versioned S3, encrypted storage, backups and alarms. The control panel is
available through an AWS-authenticated operator gateway, with SSM as a private
fallback. The participant site has no administration API.
[Internal testing](docs/aws.md#internal-test-deployment) uses SSM for both interfaces
and needs no public hostname.

Every acceptance and phase transition publishes its S3 checkpoint before success.
Final keys appear only after closure and signed-beacon finalization.
[Prepare, finish, audit and recover](docs/operations.md).

Releases include four native binaries, the browser bundle, a pinned installer,
checksums and signed provenance. The repository is internal; the deployed site
mirrors the verified participant downloads without requiring GitHub access.

**Launch gates:** complete the full core AWS/browser drill and independent security
review before opening a production campaign. Engineering tests do not replace
an external audit. [Evidence and remaining gates](docs/readiness.md).
