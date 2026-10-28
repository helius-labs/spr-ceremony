# Operator CLI

Use a verified release binary and your existing AWS SSO profile. Participants
need neither AWS access nor build tools. [Full Mac rehearsal](rehearsal.md).

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

If an open ceremony misses its deadline:

```sh
./spr-ceremony operator recover-beacon --hours 24 --reason "Contribution window exceeded the planned deadline"
```

Recovery preserves verified contributions and records the old/new beacon rounds.
All commands accept `--config FILE`; audit/export accept `--work DIRECTORY`.

For a local demo or existing SSM deployment, `operator connect` uses loopback
ports 8080/8081. AWS phase commands require the HTTPS operator endpoint.
