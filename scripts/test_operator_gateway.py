import base64
import importlib.util
import os
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('operator_gateway', Path(__file__).resolve().parents[1] / 'infra/operator/gateway.py')
gateway = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gateway)


class GatewayTests(unittest.TestCase):
    actor = 'arn:aws:sts::558215002830:assumed-role/OperatorRole/alice'

    def event(self, path, actor=None):
        event = {'version': '2.0', 'rawPath': path, 'rawQueryString': '', 'headers': {},
                 'requestContext': {'http': {'method': 'GET', 'sourceIp': '198.51.100.1'}}}
        if actor:
            event['requestContext']['authorizer'] = {'iam': {'userArn': actor}}
        return event

    def call(self, event):
        env = {'PRINCIPAL_PREFIX': self.actor.rsplit('/', 1)[0] + '/',
               'COORDINATOR_IP': '10.83.0.5', 'OPERATOR_ORIGIN': 'https://test.execute-api.eu-north-1.amazonaws.com'}
        with patch.dict(os.environ, env):
            return gateway.handler(event, None)

    def test_rejects_unsigned_identity_spoofing_and_other_roles(self):
        with patch.object(gateway.http.client, 'HTTPConnection') as connection:
            for actor in [None, self.actor.replace('558215002830', '111111111111'), self.actor.replace('OperatorRole', 'OtherRole')]:
                event = self.event('/operator/status', actor)
                event['headers']['x-spr-iam-principal'] = self.actor
                self.assertEqual(self.call(event)['statusCode'], 403)
            connection.assert_not_called()

    def test_only_verified_identity_is_forwarded_and_credentials_are_dropped(self):
        with patch.object(gateway.http.client, 'HTTPConnection') as connection:
            response = connection.return_value.getresponse.return_value
            response.status = 200
            response.read.return_value = b'{"ok":true}'
            response.getheaders.return_value = [('Set-Cookie', '__Host-SPROperator=private; Secure; HttpOnly; Path=/'), ('Content-Type', 'application/json')]
            event = self.event('/operator/status', self.actor)
            event['headers'] = {'x-spr-iam-principal': 'forged', 'authorization': 'private-signature', 'x-amz-security-token': 'private-aws-token', 'x-spr-admin': '1'}
            result = self.call(event)
            connection.assert_called_once_with('10.83.0.5', 8083, timeout=25)
            forwarded = connection.return_value.request.call_args.kwargs['headers']
            self.assertEqual(forwarded['X-SPR-IAM-Principal'], self.actor)
            self.assertEqual(forwarded['X-SPR-Gateway-IP'], '198.51.100.1')
            self.assertNotIn('authorization', forwarded)
            self.assertNotIn('x-amz-security-token', forwarded)
            self.assertEqual(result['cookies'], ['__Host-SPROperator=private; Secure; HttpOnly; Path=/'])
            self.assertEqual(base64.b64decode(result['body']), b'{"ok":true}')
            self.assertEqual(result['headers']['cache-control'], 'no-store')
            event = self.event('/admin/status')
            event['headers']['x-spr-iam-principal'] = self.actor
            self.call(event)
            self.assertNotIn('X-SPR-IAM-Principal', connection.return_value.request.call_args.kwargs['headers'])

    def test_queries_encoded_paths_and_unbounded_bodies_are_rejected(self):
        with patch.object(gateway.http.client, 'HTTPConnection') as connection:
            for path in ['/operator/../admin/status', '/%6fperator/status', '/admin/private-key', '/operator/close']:
                self.assertEqual(self.call(self.event(path, self.actor))['statusCode'], 404)
            event = self.event('/operator/status', self.actor)
            event['rawQueryString'] = 'token=private'
            self.assertEqual(self.call(event)['statusCode'], 404)
            event = self.event('/session/start')
            event['body'] = 'x' * ((4 << 20) + 1)
            self.assertEqual(self.call(event)['statusCode'], 413)
            connection.assert_not_called()
