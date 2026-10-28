# AWS deployment

One active coordinator, encrypted persistent EBS, private versioned S3, and Caddy
HTTPS. Only ports 80/443 are public. Administration uses an IAM authenticated SSM
tunnel to a separate loopback listener. No SSH keys, static AWS credentials,
invitation tokens or GitHub tokens go into Terraform or user data.

Operator tools: Terraform, AWS CLI with SSM plugin, GitHub CLI, Python 3.11+.
Participants need only a browser or the release binary.

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
The initial campaign must be `prepared`, without contributions or test markers.

From the repository root:

```sh
python3 scripts/deploy-aws.py --release RELEASE_VERSION \
  --source-sha REVIEWED_SOURCE_COMMIT --ceremony ceremony-data/core
```

The helper verifies the immutable GitHub release, provenance and hashes. It
syncs only public ceremony objects to S3, restores them on the host, runs a full
transcript audit, installs the exact release, and checks the HTTPS build and
installer. Initial preparation and audit can take substantial time. Monitor the
reported SSM command; do not start another deployment while it is running.

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
**http://127.0.0.1:8081**. Keep that exact host and port; origin checks deliberately
reject alternate hosts. The AWS CLI needs the
[Session Manager plugin](https://docs.aws.amazon.com/en_en/systems-manager/latest/userguide/session-manager-working-with-sessions-start.html).

1. Enter a stable, public pseudonym and expiration.
2. Click **Create invitation**. Copy the link and send it privately yourself.
3. Watch accepted counts. Reissue with the same pseudonym to resume expired access.
4. **Revoke** removes that invitation and its reservation. Accepted contributions remain.

Private links are shown once, cleared after five minutes, and never included in
status listings or logs. Save them privately if needed. Clipboard history and a
downloaded invitation file are under your control. Close the SSM tunnel when done.
Restrict operator IAM access to this instance and the panel session document;
SSM shell/RunCommand access is deployment-level privilege, separate from inviting.

## Finish and recover

Before the planned beacon, wait for active reservations to finish, close the
ceremony, and independently publish its closure checkpoint. After the exact
signed beacon is available, finalize, audit, verify the export, and run the real
application proof tests. See [exact commands](operations.md#close-finalize-and-independently-verify).
The public **Parameters & keys** view then exposes the final export and per-circuit
proving/verifying keys with hashes. It never treats a temporary export as final.

S3 commits every accepted contribution and lifecycle transition. Immutable
objects precede a conditional checkpoint update; a stale coordinator is fenced
out. Daily encrypted EBS snapshots retain seven days of private invitations and
local reservations. Restoring from public S3 omits invitations by design: reissue
them with the same pseudonyms. Stop/fence the previous coordinator before recovery.

Before launch, perform and record the [recovery drill](operations.md#recovery)
in the actual AWS account, verify private bucket/CORS policies, exercise an
interrupted participant upload, and confirm alert delivery. Disk alarms trigger
above 80%; health/metric gaps alarm after two five-minute periods. Preserve
independently witnessed manifest/checkpoint hashes outside the coordinator.
Engineering readiness and a deployed HTTPS site do not replace independent review.
