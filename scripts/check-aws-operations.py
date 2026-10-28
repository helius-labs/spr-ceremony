#!/usr/bin/env python3
"""Read AWS backup/alert configuration. Never reads keys or sends notifications."""
import argparse
import concurrent.futures
import datetime
import json
import subprocess
import sys


def read_json(*args):
    result = subprocess.run(args, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('Cannot read configuration; check AWS login, permissions and Terraform workspace')
    return json.loads(result.stdout)


def backup_status(snapshots, now):
    completed = [s for s in snapshots if s.get('State') == 'completed']
    timestamp = lambda s: datetime.datetime.fromisoformat(s['StartTime'].replace('Z', '+00:00'))
    latest = max(completed, key=timestamp, default=None)
    age = None
    if latest:
        age = (now - timestamp(latest)).total_seconds() / 3600
    return latest, age, bool(latest and latest.get('Encrypted') and 0 <= age <= 48)


def alert_status(alarms, subscriptions, topic, deployment):
    confirmed = [s for s in subscriptions if s.get('SubscriptionArn', '').startswith('arn:')]
    expected = {deployment + '-disk': ('DiskUsedPercent', 'GreaterThanThreshold', 80),
                deployment + '-health': ('Healthy', 'LessThanThreshold', 1)}
    found = {a['AlarmName']: a for a in alarms if a['AlarmName'] in expected}
    healthy = len(found) == 2
    for name, alarm in found.items():
        metric, comparison, threshold = expected[name]
        healthy = healthy and all([
            alarm.get('StateValue') == 'OK', alarm.get('ActionsEnabled') is True,
            topic in alarm.get('AlarmActions', []), alarm.get('TreatMissingData') == 'breaching',
            alarm.get('Namespace') == 'SPR/Ceremony', alarm.get('MetricName') == metric,
            alarm.get('ComparisonOperator') == comparison, alarm.get('Threshold') == threshold,
            alarm.get('Dimensions') == [{'Name': 'Deployment', 'Value': deployment}],
            alarm.get('Period') == 300, alarm.get('EvaluationPeriods') == 2,
        ])
    return healthy, len(confirmed)


def inspect(terraform_dir):
    outputs = read_json('terraform', '-chdir=' + terraform_dir, 'output', '-json')
    get = lambda key: outputs[key]['value']
    region = json.loads(get('s3_config'))['region']
    aws = lambda *args: read_json('aws', '--region', region, '--output', 'json', *args)
    volume = aws('ec2', 'describe-volumes', '--volume-ids', get('data_volume'))['Volumes'][0]
    deployment = next(t['Value'] for t in volume['Tags'] if t['Key'] == 'Deployment')
    topic = get('alarm_topic')
    requests = {
        'versioning': ('s3api', 'get-bucket-versioning', '--bucket', get('bucket')),
        'public': ('s3api', 'get-public-access-block', '--bucket', get('bucket')),
        'snapshots': ('ec2', 'describe-snapshots', '--owner-ids', 'self', '--filters', 'Name=volume-id,Values=' + get('data_volume')),
        'alarms': ('cloudwatch', 'describe-alarms', '--alarm-name-prefix', deployment + '-'),
        'subscriptions': ('sns', 'list-subscriptions-by-topic', '--topic-arn', topic),
        'secret': ('secretsmanager', 'describe-secret', '--secret-id', get('signing_key_secret_arn')),
        'policies': ('dlm', 'get-lifecycle-policies'),
    }
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        futures = {name: pool.submit(aws, *args) for name, args in requests.items()}
        data = {name: future.result() for name, future in futures.items()}
    now = datetime.datetime.now(datetime.timezone.utc)
    latest, age, recent = backup_status(data['snapshots']['Snapshots'], now)
    private = False
    if latest:
        permissions = aws('ec2', 'describe-snapshot-attribute', '--snapshot-id', latest['SnapshotId'], '--attribute', 'createVolumePermission')
        private = permissions.get('CreateVolumePermissions') == []
    healthy, confirmed = alert_status(data['alarms']['MetricAlarms'], data['subscriptions']['Subscriptions'], topic, deployment)
    policies = [aws('dlm', 'get-lifecycle-policy', '--policy-id', p['PolicyId'])['Policy']
                for p in data['policies']['Policies'] if p.get('Tags', {}).get('Deployment') == deployment]
    scheduled = any(p['State'] == 'ENABLED'
                    and p['PolicyDetails'].get('TargetTags') == [{'Key': 'SPRBackup', 'Value': deployment}]
                    and any(s['CreateRule'].get('Interval') == 24 and s['CreateRule'].get('IntervalUnit') == 'HOURS'
                            and s['RetainRule'].get('Count', 0) >= 7 for s in p['PolicyDetails']['Schedules'])
                    for p in policies)
    public = data['public']['PublicAccessBlockConfiguration']
    secret = data['secret']
    checks = {
        's3_versioning': data['versioning'].get('Status') == 'Enabled',
        's3_public_access_block': all(public.get(k) is True for k in ['BlockPublicAcls', 'IgnorePublicAcls', 'BlockPublicPolicy', 'RestrictPublicBuckets']),
        'data_volume_encrypted': volume.get('Encrypted') is True,
        'daily_backup_policy': scheduled and {'Key': 'SPRBackup', 'Value': deployment} in volume['Tags'],
        'completed_backup_within_48_hours': recent,
        'latest_backup_private': private,
        'health_and_disk_alarms': healthy,
        'confirmed_alert_recipient': confirmed > 0,
        'signing_key_backup_metadata': not secret.get('DeletedDate') and any('AWSCURRENT' in stages for stages in secret.get('VersionIdsToStages', {}).values()),
    }
    return {
        'checked_at': now.isoformat(), 'deployment': deployment,
        'checks_passed': all(checks.values()), 'checks': checks,
        'latest_completed_backup': latest['SnapshotId'] if latest else None,
        'pending_backups': sum(s.get('State') == 'pending' for s in data['snapshots']['Snapshots']),
        'backup_age_hours': round(age, 2) if age is not None else None,
        'confirmed_alert_recipients': confirmed,
        'limits': ['Configuration checks do not prove restore success or notification delivery.',
                   'No signing key values or recipient addresses are read into this report.',
                   'This does not verify the ceremony transcript or final keys.'],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--terraform-dir', default='infra/aws')
    args = parser.parse_args()
    try:
        report = inspect(args.terraform_dir)
    except (RuntimeError, ValueError, KeyError, StopIteration, OSError) as error:
        print('AWS operations check could not complete: ' + str(error), file=sys.stderr)
        return 2
    print(json.dumps(report, indent=2))
    return 0 if report['checks_passed'] else 1


if __name__ == '__main__':
    sys.exit(main())
