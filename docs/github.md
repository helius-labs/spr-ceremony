# GitHub account linking

Optional, after completing all invited core circuits. Contributions do not require
a GitHub account. Linking does not change circuit state, receipts or checkpoints.

## Contributor

After completion, the CLI prints a private **Link GitHub** URL. Open it, continue
with GitHub, check the displayed account and contributor, then choose **Publish
link**. The link expires after ten minutes and can be opened once.

Already finished? With a current binary:

```sh
./spr-ceremony link-github
# Paste your original invitation at the hidden prompt.
```

Or use `./spr-ceremony link-github --invitation alice.json`. The invitation must
still be valid. If it expired, ask the operator to reissue the same invitation ID
while the ceremony is open. Reissuing preserves accepted work.

The site displays the account link immediately. The existing GitHub publication
workflow verifies it and updates [`attestations/`](../attestations/) automatically
on its next scheduled run (scheduled every 15 minutes; GitHub can delay jobs).
No participant PR or manual JSON copying is needed. Unlinked participants remain
in the public contribution records.

One invitation ID can be linked to one numeric GitHub account ID. Repeating the
flow with that account is harmless; it cannot replace the published identity.
The login is recorded at linking time and may later change on GitHub. This proves
the coordinator observed account control through GitHub OAuth. It is **not a
personal participant signature**, proof of secret erasure or an MPC audit.

## Operator setup

Create a dedicated **OAuth App** owned by `helius-labs`, through the organization's
Settings → Developer settings → OAuth Apps. Use:

| Field | Value |
| --- | --- |
| Application name | Solana Privacy Rings |
| Homepage URL | `https://d3gz3drevfa8mb.cloudfront.net` |
| Authorization callback URL | `https://d3gz3drevfa8mb.cloudfront.net/github/callback` |
| Device flow | Disabled |

Generate a client secret. Keep it out of chat, Git, shell history and Terraform
variables/state. The helper reads it at a hidden prompt on your own machine:

```sh
python3 scripts/configure-github-oauth.py \
  --profile AdministratorAccess-558215002830 --region eu-north-1
```

The helper checks AWS login and secret metadata before asking for credentials.
If SSO has expired, run `aws sso login --profile AdministratorAccess-558215002830`
and retry. Add `--check` to diagnose AWS access without prompting for or changing
any secrets. Existing secret names are never overwritten.

It creates a Secrets Manager secret and prints only its ARN. It does not deploy
or restart anything. Add that ARN as `github_oauth_secret_arn` in the private AWS
Terraform configuration. Review/apply the infrastructure plan, then deploy a
release containing this feature during the agreed maintenance window. An empty
ARN disables linking. Contributions remain available without this feature.

The instance role receives access only to that secret. At startup, its loader
creates `/var/lib/spr/github/oauth.json` with private permissions. `serve` receives
`--github-oauth-config /var/lib/spr/github/oauth.json`. Both new-instance bootstrap
and `scripts/deploy-aws.py` configure this; existing instances need deployment.

The CDN must disable caching and forward query strings, cookies and headers for
the callback and `/v1/github/*`. The repository's CloudFront AllViewer policy
already forwards these. Do not enable request-body logging or retain OAuth
callback query strings in access logs. The callback redirects to a clean URL.

Verify a real account linking flow after activation. Local tests use a simulated
provider and do not establish that the real app's settings/credentials are correct.

## Public proof and recovery

`GET /v1/github/identities/INVITATION_ID` returns a separate coordinator-signed
record binding the manifest, invitation ID, contributor, receipt digest, numeric
GitHub ID, login and timestamp. Verification uses the manifest's OpenPGP public
key and the `github-identity` signing domain.

```sh
./spr-ceremony records \
  --url https://d3gz3drevfa8mb.cloudfront.net \
  --manifest-sha256 AUTHENTICATED_MANIFEST_SHA256 \
  --out public-records --github-out github-identities
```

This verifies receipts and account signatures and writes public JSON under
`github-identities/<manifest>/<invitation-id>.json`. Existing records are never
overwritten. The repository archives these in `contributions/github/` and links
them from `attestations/README.md`.

On AWS, the signed artifact is stored at `objects/<sha256>` and an immutable
pointer at `campaigns/<manifest>/github-identities/<invitation-id>.json`, inside
the configured S3 prefix. Publication succeeds only after S3 commits. Restored
coordinators recover sidecars on demand using the known invitation IDs. These
optional records are separate from a pinned MPC snapshot; archive them alongside
the receipt records when making an offline copy.

OAuth state, PKCE verifiers and invitation credentials stay in bounded temporary
server memory. Provider tokens are used to read the public profile and are not
persisted. Restarting invalidates unfinished linking sessions; rerun `link-github`.

GitHub documents the [authorization flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps),
[public-only scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps)
and [app registration](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app).
