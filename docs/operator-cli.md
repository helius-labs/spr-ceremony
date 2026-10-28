# Operator CLI

Use a verified release binary and your existing AWS SSO profile. Participants
need neither AWS access nor build tools. [Mac ceremony guide](rehearsal.md).

## Connect and open

```sh
aws sso login --profile YOUR_PROFILE
./spr-ceremony operator connect --operator-url https://API_ID.execute-api.REGION.amazonaws.com --profile YOUR_PROFILE
./spr-ceremony operator open --hours 24
```

The connection file stores public addresses and a manifest pin. AWS manages its
own credential cache. Existing files are never overwritten; reconnect a changed
campaign using `--config NEW_FILE`. Opening announces a future quicknet round.
Close before that deadline and finalize after it.

## Invite and contribute

```sh
./spr-ceremony operator invite sergei --out invitations/sergei.json
./spr-ceremony contribute --invitation invitations/sergei.json
```

For someone else, run `operator invite bob` and privately send the generated
file and printed command. The file has mode 0600; its secret never appears in
the command. Invitations last 24 hours by default; `--hours 72` changes that.

For a copyable invitation **link**, open the hosted control panel, sign in by
running its displayed `operator login CODE` command, then enter the contributor
name and choose **Create invitation → Copy link**. Send the link privately with
the [README contributor one-liner](../README.md#contribute). It downloads the
native client and prompts for the link; no compilation or AWS login is needed.
`operator invite` itself saves a JSON file and does not print a bearer link.

The client queues, contributes, verifies, uploads and waits for acceptance for
each circuit. Rerun after interruption. Accepted work is skipped. Contributors
may run concurrently; each circuit accepts one contributor at a time.

## Check and manage

```sh
./spr-ceremony operator status
./spr-ceremony operator audit
./spr-ceremony operator reissue bob
./spr-ceremony operator revoke bob
```

Status shows the deadline, reservations and accepted counts. Reissue replaces
access while preserving identity and progress; revoke retains accepted work.
Use a unique pseudonym or invitation ID. Put it before flags.

Audit pins the current checkpoint through AWS authentication, downloads public
objects and independently checks signatures, Phase 1 derivation, initialization,
contributions and checkpoint history. **Full-core audits can take hours.** Keep
`audit/` to reuse downloads. It contains no private invitations or signing keys.

For parallel verification, use `operator audit --workers 4`. Each worker needs
additional RAM. Audit prints circuit starts, completed counts and elapsed times;
it checks every circuit with the same pinned engine and initialization rules.

## Public audit

Anyone with the CLI can audit a chosen checkpoint without an invitation, AWS
credentials or operator configuration:

```sh
./spr-ceremony audit --url https://d3gz3drevfa8mb.cloudfront.net \
  --manifest-sha256 MANIFEST_SHA256 --checkpoint-sha256 CHECKPOINT_SHA256 \
  --work audit --workers 4
```

Replace both hashes with values authenticated through the operator or an
independently witnessed publication. Operators can obtain them with
`operator status --json` (`manifest.sha256` and `checkpoint.sha256`). Downloading
hashes from the same untrusted server alone does not authenticate the ceremony.

The command verifies Phase 1 derivation, circuit initialization, every included
contribution, receipt signatures and checkpoint history. It stays on the requested
checkpoint even while new contributions arrive. The workspace contains public
artifacts only; interrupted downloads resume and completed downloads are reused.
Cryptographic verification runs again on each invocation and can take hours for
the full core set. `--workers` defaults to 1; more workers require more RAM.
An audit does not close the ceremony or verify final exported keys.

## Contribution records

Completed core contributions appear automatically in the participant site's
**Contributions** section. Records contain the checkpoint, public artifact hashes
and coordinator-signed receipts. They are labeled **accepted**; full transcript
audit remains separate. Partial participation and invitation tokens are excluded.

The **Contribution records** GitHub workflow independently checks the pinned
manifest, checkpoint state hashes and receipt signatures before archiving new
records under `contributions/records/<manifest>/<invitation-id>.json`. Existing
records are preserved. It runs about every 15 minutes when the repository
variable `SPR_RECORD_PUBLICATION_ENABLED` is `true`, or on manual dispatch.
The coordinator has no GitHub credential; a failed mirror never blocks acceptance.

To perform the same public-data export locally, without AWS credentials:

```sh
./spr-ceremony records --url https://CEREMONY_HOST \
  --manifest-sha256 AUTHENTICATED_MANIFEST --out contribution-records
```

This checks receipt authentication and inclusion at the downloaded checkpoint;
it does not replace `operator audit`. The workflow's source pins must be reviewed
and updated explicitly for a new ceremony.

## Finish

```sh
./spr-ceremony operator close
# After the announced beacon time:
./spr-ceremony operator finalize
./spr-ceremony operator export --out final-keys
```

Phase changes run on AWS and continue if the terminal disconnects. Use
`operator job` to check the latest operation. Finalization publishes keys to S3;
export downloads and independently reproduces them locally. Existing export
files are checked, never replaced. This verification also takes substantial time.

Use `operator export --out final-keys --workers 4` to rebuild circuits in parallel
on your computer, allowing RAM for each worker. Hosted finalization uses the
server's configured worker count; the operator command stays the same. See
[AWS resource sizing](aws.md#finalization-resources).

If an open ceremony misses its deadline:

```sh
./spr-ceremony operator recover-beacon --hours 24 --reason "Contribution window exceeded the planned deadline"
```

Recovery preserves verified contributions and records the old/new beacon rounds.
The new deadline is measured from the recovery command. Contributions pause at
the original deadline until recovery succeeds. Recovery requires the ceremony to
remain open: it cannot extend an unexpired deadline or change a closed or
finalized ceremony. Invitation expiry is separate; reissue expired invitations
to restore access without losing accepted work.
All commands accept `--config FILE`; audit/export accept `--work DIRECTORY`.

For a local demo or existing SSM deployment, `operator connect` uses loopback
ports 8080/8081. AWS phase commands require the HTTPS operator endpoint.
