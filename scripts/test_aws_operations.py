import copy
import datetime
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('operations', Path(__file__).with_name('check-aws-operations.py'))
operations = importlib.util.module_from_spec(spec)
spec.loader.exec_module(operations)


class AWSOperationsTests(unittest.TestCase):
    def test_only_recent_completed_encrypted_backups_pass(self):
        now = datetime.datetime(2026, 10, 28, 12, tzinfo=datetime.timezone.utc)
        good = {'SnapshotId': 'snap-good', 'State': 'completed', 'Encrypted': True,
                'StartTime': '2026-10-28T03:00:00Z'}
        self.assertTrue(operations.backup_status([good], now)[2])
        cases = [[], [dict(good, State='pending')], [dict(good, Encrypted=False)],
                 [dict(good, StartTime='2026-10-25T03:00:00Z')],
                 [dict(good, StartTime='2026-10-29T03:00:00Z')]]
        for snapshots in cases:
            with self.subTest(snapshots=snapshots):
                self.assertFalse(operations.backup_status(snapshots, now)[2])
        pending = dict(good, State='pending', StartTime='2026-10-28T11:00:00Z')
        latest, _, passed = operations.backup_status([good, pending], now)
        self.assertEqual(latest['SnapshotId'], 'snap-good')
        self.assertTrue(passed)

    def test_alert_actions_health_and_recipient_are_distinct(self):
        topic = 'arn:aws:sns:eu-north-1:123456789012:alerts'
        alarms = []
        for suffix, metric, comparison, threshold in [('disk', 'DiskUsedPercent', 'GreaterThanThreshold', 80), ('health', 'Healthy', 'LessThanThreshold', 1)]:
            alarms.append({'AlarmName': 'ceremony-' + suffix, 'Namespace': 'SPR/Ceremony',
                           'MetricName': metric, 'ComparisonOperator': comparison, 'Threshold': threshold,
                           'Dimensions': [{'Name': 'Deployment', 'Value': 'ceremony'}],
                           'Period': 300, 'EvaluationPeriods': 2, 'TreatMissingData': 'breaching',
                           'StateValue': 'OK', 'ActionsEnabled': True, 'AlarmActions': [topic]})
        confirmed = [{'SubscriptionArn': topic + ':subscription'}]
        self.assertEqual(operations.alert_status(alarms, confirmed, topic, 'ceremony'), (True, 1))
        self.assertEqual(operations.alert_status(alarms, [], topic, 'ceremony'), (True, 0))
        self.assertEqual(operations.alert_status(alarms, [{'SubscriptionArn': 'PendingConfirmation'}], topic, 'ceremony'), (True, 0))
        for patch in [{'ActionsEnabled': False}, {'AlarmActions': []}, {'StateValue': 'INSUFFICIENT_DATA'},
                      {'TreatMissingData': 'notBreaching'}, {'Dimensions': []}, {'MetricName': 'WrongMetric'}]:
            with self.subTest(patch=patch):
                changed = copy.deepcopy(alarms)
                changed[0].update(patch)
                self.assertEqual(operations.alert_status(changed, confirmed, topic, 'ceremony'), (False, 1))
        self.assertEqual(operations.alert_status(alarms[:1], confirmed, topic, 'ceremony'), (False, 1))


if __name__ == '__main__':
    unittest.main()
