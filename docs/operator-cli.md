# Operator CLI

Use a verified release binary. No Go or S3 credentials are needed on your laptop.
AWS CLI and its Session Manager plugin authenticate the private control tunnel.

## Connect once

For internal AWS testing, keep the participant (8080) and control (8081) SSM
tunnels open in separate terminals. Use the instance and document names from
the [AWS guide](aws.md#internal-test-deployment).

```sh
./spr-ceremony operator connect
```

This checks both endpoints and saves `ceremony-operator.json`, containing the
addresses and pinned manifest digest. It contains no credentials. A changed
campaign requires an explicit new connection file; existing files are never
overwritten. For a public participant site, use
`operator connect --url https://CEREMONY_HOST`.

## Invite and contribute

```sh
./spr-ceremony operator invite bob
```

The command saves a new mode-0600 file under `invitations/` and prints a command
using its filename. Send the file and command privately to Bob. On his computer,
Bob saves the invitation beside the binary and runs that command, for example:

```sh
./spr-ceremony contribute --invitation invitation-123456.json
```

The actual filename is printed by `invite`. To choose it yourself, use
`operator invite bob --out invitations/bob.json`. Invitation secrets never appear
in the generated command. Internal-test contributors also need the participant
SSM tunnel; they do not need access to the control panel.

The client joins the queue, adds randomness, verifies locally, uploads, and waits
for acceptance for each circuit. Rerun the same command after an interruption.
Accepted work is skipped. Independent contributors may run concurrently; each
circuit permits one contributor at a time.

## Check and manage

```sh
./spr-ceremony operator status
./spr-ceremony operator audit
./spr-ceremony operator reissue bob
./spr-ceremony operator revoke bob
```

`status` shows invitation IDs, accepted counts and current reservations. Use a
unique name or the displayed ID for reissue/revoke. Reissue saves a new private
file, invalidates the old link and preserves progress. Revoke keeps accepted work.
New invitations last 24 hours by default; `--hours 72` changes that limit.

`audit` pins the current checkpoint through the authenticated control tunnel,
downloads public objects and verifies signatures, Phase 1 derivation, contributions
and checkpoint history locally. It verifies that snapshot even if other people
contribute afterward. The full core transcript requires several GB of disk and
substantial computation. Keep `audit/` to reuse downloads. No private invitations,
signing keys or AWS credentials are saved there.

All commands accept `--config FILE`; audit accepts `--work DIRECTORY`.
Put a contributor name or ID before flags, for example
`operator reissue bob --out invitations/bob-renewed.json`.

## Finish

Closing and finalizing remain explicit coordinator operations. The invitation
control API cannot open, close or finalize a ceremony. Follow the
[close, finalize and verify commands](operations.md#close-finalize-and-independently-verify)
on the AWS coordinator through an authorized SSM shell. Final keys appear only
after finalization. Audit the transcript and verify the export before using them.
