# Full-core rehearsal

41 real core circuits, public Phase 1 parameters, and a fresh coordinator signing
key. You operate and contribute from your Mac. No SSM sessions are needed.
This campaign is explicitly marked as a test; its keys are not for production.

- Participants: https://d3gz3drevfa8mb.cloudfront.net
- Control: https://1q82ow4q4c.execute-api.eu-north-1.amazonaws.com

## Connect

For Apple silicon, use a fresh working folder:

```sh
mkdir -p ~/spr-core-rehearsal
cd ~/spr-core-rehearsal
curl --proto '=https' --tlsv1.2 -fsS https://d3gz3drevfa8mb.cloudfront.net/native/spr-ceremony-darwin-arm64 -o spr-ceremony
chmod +x spr-ceremony
./spr-ceremony version
aws sso login --profile AdministratorAccess-558215002830
./spr-ceremony operator connect --operator-url https://1q82ow4q4c.execute-api.eu-north-1.amazonaws.com --profile AdministratorAccess-558215002830
```

Use `darwin-amd64` for Intel Macs. [Verify the release](releases.md) independently
if desired. Your saved operator profile contains addresses and public pins;
AWS keeps its own SSO credential cache. Participants need no AWS account.

## Open, invite, contribute

```sh
./spr-ceremony operator open --hours 24
./spr-ceremony operator status
./spr-ceremony operator invite sergei --out invitations/sergei.json
./spr-ceremony contribute --invitation invitations/sergei.json
./spr-ceremony operator audit
```

Opening announces a quicknet round 24 hours ahead. **Close before that time;
finalize after it.** Keep your Mac awake while contributing. Rerun the same
contribution command after interruption. Completion is **41/41 accepted**.
The full audit downloads several GB and reconstructs the circuit parameters;
it can take hours. Keep `work/` and `audit/` to reuse public data.

For another person, run `operator invite bob`, then send the generated private
file and printed command. Bob can also open the public site and select that file.
To send a browser link, use the control panel.

## Control panel

Open the control URL and click **Sign in**. It shows a command such as:

```sh
./spr-ceremony operator login ABCD-EFGH-JKLM
```

Run the actual command shown in your own browser. Your AWS identity approves
that browser for 30 minutes. The displayed code alone grants no access; the
session cookie is Secure, HttpOnly and confined to the control origin. AWS
credentials and coordinator signing keys never enter the browser.

Create, reissue or revoke invitations there. Reissuing preserves progress and
invalidates the old invitation. Revoking keeps accepted contributions.

## Finish

When all intended contributors have finished and your audit passes:

```sh
./spr-ceremony operator status
./spr-ceremony operator close
```

After the exact planned beacon time:

```sh
./spr-ceremony operator finalize
./spr-ceremony operator export --out final-keys
```

Finalization runs on AWS and publishes the keys to S3. Export downloads the final
public snapshot and independently verifies the keys on your Mac. Neither step
needs an SSM session. Long operations continue on AWS if you close your terminal:

```sh
./spr-ceremony operator job
```

The participant site's **Parameters & keys** section also lists final keys.
Keep the verified keys, snapshot and participant receipts.

If you miss the closure deadline, preserve contributions and explicitly announce
a later quicknet round:

```sh
./spr-ceremony operator recover-beacon --hours 24 --reason "Contribution window exceeded the planned deadline"
```

This requires an open campaign and a missed deadline. Review the new deadline
with `operator status`, then close before it.
