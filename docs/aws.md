# AWS deployment

One active coordinator, encrypted persistent EBS, private versioned S3, and Caddy
HTTPS. Direct hosting exposes 80/443; CloudFront hosting uses a private VPC origin.
Administration uses the [AWS operator gateway](../infra/operator/README.md) or
an IAM authenticated SSM tunnel. No SSH keys, static AWS credentials,
invitation tokens or GitHub tokens go into Terraform or user data.

Operator tools: Terraform, AWS CLI with SSM plugin, GitHub CLI, Python 3.11+.
Participants use the release binary or the hosted terminal installer.

## Finalization resources

Set Terraform `finalize_workers` to the number of core circuits the operator
service should rebuild concurrently (1–41, default 1). Deployment configures
`SPR_FINALIZE_WORKERS`, four Go execution slots per worker, and four GiB per
worker with a 12 GiB minimum memory limit. Terraform checks the instance has
that CPU budget and memory plus four GiB for other services. For example,
24 workers reserve 96 vCPUs and a 96 GiB service memory limit.

Apply the reviewed Terraform plan, then deploy the release to update existing
services. A worker-count change alone does not restart the live coordinator.
Keep contributors paused during deployment. Finalization still verifies the
announced beacon and all contributions; parallel scheduling does not change key
bytes or skip initialization. Each circuit has sequential work, so adding workers
does not guarantee proportional speedups. This option does not resize the VM.

For the public ceremony with CLI operation and no tunnels, follow the
[Mac walkthrough](rehearsal.md). Infrastructure setup is documented in
[public-test](../infra/public-test/README.md).

## Internal test deployment

No hostname is needed. Set `access_mode = "ssm"` in a separate Terraform
workspace. This creates no inbound security-group rules or Elastic IP; the
instance's outbound public IP is used for AWS APIs and updates. Both services
listen only on loopback. Internal testers need AWS access and the
[Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html).

On a Mac with Homebrew, install it once:

```sh
brew install --cask session-manager-plugin
session-manager-plugin --version
```

```sh
export AWS_PROFILE=dev
export AWS_REGION=eu-north-1
cd infra/aws
terraform init
terraform workspace new internal-test  # use workspace select on later visits
```

Create ignored `internal.tfvars`:

```hcl
region      = "eu-north-1"
name        = "spr-ceremony-test"
access_mode = "ssm"
data_gib    = 100
```

```sh
terraform plan -var-file=internal.tfvars -out=internal.tfplan
terraform apply internal.tfplan
```

From the repository root, use a release binary to prepare a marked rehearsal,
then deploy it. `--rehearsal` requires a marked test campaign and either SSM or
the dedicated CloudFront rehearsal mode; it refuses direct production hosting.

```sh
./spr-ceremony demo --out rehearsal/aws
TF_WORKSPACE=internal-test python3 scripts/deploy-aws.py \
  --release RELEASE_VERSION --source-sha REVIEWED_SOURCE_COMMIT \
  --ceremony rehearsal/aws --rehearsal
```

Use `terraform output instance_id` in the selected workspace. Keep these two
commands running in separate terminals. Paste each command as one complete line;
free local ports 8080 and 8081 first. Set `AWS_PROFILE` to your configured profile
in both terminals, or add `--profile YOUR_PROFILE` to each command.

```sh
aws ssm start-session --target INSTANCE_ID --document-name spr-ceremony-test-participant
aws ssm start-session --target INSTANCE_ID --document-name spr-ceremony-test-control-panel
```

Open **http://127.0.0.1:8081** to create an invitation; its link opens the
participant site on **http://127.0.0.1:8080**. Use these exact ports. The HTTP
connections are local to each end of the authenticated SSM tunnel. The site
labels the campaign as a test. Participants use release binaries;
the HTTPS terminal installer remains available only on a public HTTPS deployment.
Do not expose the forwarded ports to your LAN.

This is a separate campaign, bucket and volume. Do not promote rehearsal keys
or switch this live workspace to public mode. Provision production separately.
Stop the instance when the test is idle to stop compute charges; EBS, S3 and
snapshots remain billable and retain state. Starting it again keeps both tunnel
commands unchanged. Terraform deliberately protects the volume and bucket from
accidental deletion.

With both tunnels open, the optional `scripts/test-aws-browser.py --binary
/path/to/verified/spr-ceremony --out /tmp/spr-aws-checks` check exercises Chromium,
WebKit, CLI invitation handoff, native participation, retry and invitation revocation.
It requires the browser tools from `scripts/browser-requirements.txt` and refuses
anything except an open two-circuit marked demo. It adds three test contributors;
its output contains public receipts and screenshots, never private invitations.

## Provision

Choose a public hostname you control, region and operating budget. Default
capacity is an x86 `m7i.xlarge` with 16 GiB RAM and 200 GiB data storage. Account
for compute, EBS and snapshots, S3 growth and participant bandwidth. Terraform
creates an isolated VPC; it does not modify existing application infrastructure.

```sh
export AWS_PROFILE=dev
cd infra/aws
terraform init
```

Create ignored `production.tfvars`:

```hcl
region      = "eu-north-1"
domain      = "YOUR_PUBLIC_HOSTNAME"
name        = "spr-ceremony"
alarm_email = "YOUR_OPERATOR_EMAIL"
```

```sh
terraform plan -var-file=production.tfvars -out=production.tfplan
terraform apply production.tfplan
terraform output public_ip
```

Point the hostname's public A record at that Elastic IP. Caddy obtains its TLS
certificate automatically; allow DNS propagation. Confirm the SNS email
subscription. Without a confirmed subscription, alarms cannot notify anyone.
Back up Terraform state in encrypted, access-controlled storage; never commit
state, plans or variable files. Data volumes have destruction protection.

## Deploy the release and real circuits

Prepare the production core directory following the [operator runbook](operations.md).
Use the pinned public Phase 1 SRS and the reviewed 41-circuit compilation.
The initial campaign must be `prepared`, with a manifest-bound coordinator public
key and without contributions or test markers.

From the repository root:

```sh
python3 scripts/deploy-aws.py --release RELEASE_VERSION \
  --source-sha REVIEWED_SOURCE_COMMIT --ceremony ceremony-data/core \
  --signing-key ceremony-data/core.private.asc
```

The helper verifies the immutable GitHub release, provenance and hashes. It
syncs only public ceremony objects to S3, restores them on the host, runs a full
transcript audit, installs the exact release, and checks the HTTPS build and
installer. The matching private signing key is stored in Secrets Manager with
its version ID equal to the manifest SHA256. Only secret metadata enters
Terraform state. Key bytes never enter S3 bundles, SSM commands or logs. Services
retrieve that exact version into a 0600 file on encrypted EBS before starting;
the binary checks it against the manifest. Retain an independent private-key
backup and restrict Secrets Manager/SSM administration.
Initial preparation and audit can take substantial time. Monitor the
reported SSM command; do not start another deployment while it is running.

For a **prepared, marked rehearsal only**, `--rehearsal-background-audit` freezes
the restored snapshot and starts its initialization audit as a separate systemd
unit. Hash checks still complete before activation. Contributions are provisional
until this audit passes; inspect the reported unit with `systemctl status` and
`journalctl -u`. Production deployments cannot use this option. Finalization
still independently verifies all initialization and contribution mathematics.

For software updates, omit `--ceremony`. Existing invitations and accepted work
remain on EBS. Update during a planned pause: a service restart interrupts active
uploads, which clients retry from saved public output. Retain the prior version
under `/opt/spr/releases/`. Roll back only to a version compatible with the current
checkpoint format; stop both services before changing the `current` symlink.
Terraform ignores later AMI/user-data changes to avoid replacing a live
coordinator: review OS/configuration updates explicitly through SSM.

The default state is **prepared**. Authenticate and announce a future drand
beacon plan, then use the operator CLI to open the campaign. Publish the manifest,
opening checkpoint and planned deadline independently of this host. The panel
enables invitations only after opening. Never use synthetic test beacons in production.

## Invite from the control panel

```sh
aws ssm start-session --target INSTANCE_ID --document-name spr-ceremony-control-panel
```

Use the actual `instance_id` and `panel_document` Terraform outputs. Open
**http://127.0.0.1:8081**. The `localhost:8081` landing page redirects here;
administration requests still require the exact numeric origin. The AWS CLI needs the
[Session Manager plugin](https://docs.aws.amazon.com/en_en/systems-manager/latest/userguide/session-manager-working-with-sessions-start.html).

1. Enter a stable, public pseudonym and expiration. Other contributors can still be working.
2. Click **Create invitation**. The dialog names the contributor. Copy the link,
   send it privately yourself, then click **Done**.
3. For an existing pseudonym, use **Reissue link**. It creates a new 24-hour link,
   preserves the invitation ID and progress, and invalidates the old link.
4. **Revoke**, then **Revoke access**, disables the link and releases its reservation.
   Accepted contributions remain visible with **Revoked** status.

Private links are shown once, cleared after five minutes, and never included in
status listings or logs. Save them privately if needed. Clipboard history and a
downloaded invitation file are under your control. Close the SSM tunnel when done.
Restrict operator IAM access to this instance and the panel session document;
SSM shell/RunCommand access is deployment-level privilege, separate from inviting.

The [operator CLI](operator-cli.md) uses the same tunnel to create private
invitation files, reissue, revoke, inspect progress and independently audit the
public transcript. Internal testing requires both SSM tunnels for these commands.

## Finish and recover

Before the planned beacon, wait for active reservations to finish, close the
ceremony, and independently publish its closure checkpoint. After the exact
signed beacon is available, finalize, audit, verify the export, and run the real
application proof tests. See [exact commands](operations.md#close-finalize-and-independently-verify).
The public **Parameters & keys** view then exposes the final export and per-circuit
proving/verifying keys with hashes. It never treats a temporary export as final.

S3 commits every accepted contribution and lifecycle transition. Immutable
objects precede a conditional checkpoint update; a stale coordinator is fenced
out. Daily encrypted EBS snapshots retain seven days of private invitations,
reservations and the local signing-key copy; restrict snapshot access accordingly.
Restoring from public S3 omits invitations by design: reissue the original IDs
from the control panel. The same signing-key version is restored independently
from Secrets Manager. Stop/fence the previous coordinator before recovery.

Before launch, perform and record the [recovery drill](operations.md#recovery)
in the actual AWS account, verify private bucket/CORS policies, exercise an
interrupted participant upload, and confirm alert delivery. Disk alarms trigger
above 80%; health/metric gaps alarm after two five-minute periods. Preserve
independently witnessed manifest/checkpoint hashes outside the coordinator.
Engineering readiness and a deployed HTTPS site do not replace independent review.

Check the deployed backup and alert configuration with your AWS operator login:

```sh
AWS_PROFILE=YOUR_PROFILE python3 scripts/check-aws-operations.py
```

Use the same `TF_WORKSPACE` as deployment (`internal-test` for the current hosted
campaign). This read-only check exits nonzero if a completed encrypted backup is
missing or older than 48 hours, snapshot access is shared, alarms are unhealthy,
or no alert recipient has confirmed their subscription. It also checks S3
versioning/public-access blocking, the daily backup policy and signing-key backup
metadata. It never reads private key values, prints recipient addresses or sends
messages. A passing report does not prove that restoration or alert delivery works.

The daily snapshot schedule does not provide an immediate first backup. After
initial deployment, create a private encrypted snapshot of the Terraform
`data_volume` output and wait for completion. Keep it until a newer backup has
been verified; manually created snapshots are not removed by the daily policy.

## Upgrade the internal unsigned prototype

Old prototype invitations and receipts predate signatures and unique IDs. They
cannot be retroactively authenticated. Preserve the old test campaign and start
a fresh marked rehearsal:

```sh
./spr-ceremony demo --out ceremony-data/signed-rehearsal
python3 scripts/deploy-aws.py --release RELEASE_VERSION --source-sha REVIEWED_SOURCE_COMMIT \
  --ceremony ceremony-data/signed-rehearsal --rehearsal \
  --replace-test-manifest EXACT_OLD_TEST_MANIFEST_SHA256
```

This option is restricted to SSM deployments and explicitly marked test data.
The host archives the old store at `replaced-<manifest>`; its S3 history remains.
It refuses a production campaign or a mismatched old digest. New invitation
links are required. Normal upgrades omit the replacement and ceremony flags.
