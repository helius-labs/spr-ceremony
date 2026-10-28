"""Bounded private proxy. Never log events, cookies, signatures or responses."""
import base64
import http.client
import ipaddress
import os
import re
from urllib.parse import urlsplit

LIMIT = 4 << 20
PUBLIC = {'/', '/admin.js', '/style.css', '/session/start', '/session/status', '/session/poll', '/session/logout',
          '/admin/status', '/admin/invite', '/admin/reissue', '/admin/revoke'}
AUTHENTICATED = {'/operator/config', '/operator/status', '/operator/invite', '/operator/reissue', '/operator/revoke', '/operator/login', '/operator/run', '/operator/job'}


def reject(status, message):
    return {'statusCode': status, 'headers': {'cache-control': 'no-store', 'content-type': 'text/plain'}, 'body': message}


def handler(event, context):
    try:
        request = event.get('requestContext', {})
        path = event.get('rawPath', '')
        if event.get('version') != '2.0' or event.get('rawQueryString') or path not in PUBLIC | AUTHENTICATED:
            return reject(404, 'Not found')
        actor = ''
        if path in AUTHENTICATED:
            iam = request.get('authorizer', {}).get('iam', {})
            actor = iam.get('userArn', '')
            if not actor.startswith(os.environ['PRINCIPAL_PREFIX']) or '/' in actor[len(os.environ['PRINCIPAL_PREFIX']):] or len(actor) <= len(os.environ['PRINCIPAL_PREFIX']):
                return reject(403, 'AWS operator identity required')
        method = request.get('http', {}).get('method', '')
        if method not in {'GET', 'POST'}:
            return reject(405, 'Method not allowed')
        body = event.get('body') or ''
        if not isinstance(body, str) or len(body) > 2 * LIMIT:
            return reject(413, 'Request too large')
        body = base64.b64decode(body, validate=True) if event.get('isBase64Encoded') else body.encode('utf-8')
        if len(body) > LIMIT:
            return reject(413, 'Request too large')
        source = str(ipaddress.ip_address(request.get('http', {}).get('sourceIp', '')))
        forwarded = {'Host': urlsplit(os.environ['OPERATOR_ORIGIN']).netloc, 'X-SPR-Gateway-IP': source}
        headers = event.get('headers') or {}
        # The actor can only come from API Gateway's IAM authorizer. Never pass
        # through caller-supplied identity or forwarding headers.
        for name in ['origin', 'content-type', 'x-spr-admin', 'x-spr-manifest', 'sec-fetch-site']:
            if name in headers:
                forwarded[name] = headers[name]
        if actor:
            forwarded['X-SPR-IAM-Principal'] = actor
        cookies = event.get('cookies') or []
        if cookies:
            cookie = '; '.join(cookies)
            if len(cookie) > 4096:
                return reject(400, 'Invalid cookies')
            forwarded['Cookie'] = cookie
        connection = http.client.HTTPConnection(os.environ['COORDINATOR_IP'], 8083, timeout=25)
        try:
            connection.request(method, path, body=body, headers=forwarded)
            response = connection.getresponse()
            content = response.read(LIMIT + 1)
            if len(content) > LIMIT:
                return reject(502, 'Invalid coordinator response')
            output, cookies = {}, []
            for name, value in response.getheaders():
                name = name.lower()
                if name == 'set-cookie':
                    cookies.append(value)
                elif name not in {'connection', 'transfer-encoding', 'content-length', 'server', 'date'}:
                    output[name] = value
            output['cache-control'] = 'no-store'
            return {'statusCode': response.status, 'headers': output, 'cookies': cookies,
                    'body': base64.b64encode(content).decode('ascii'), 'isBase64Encoded': True}
        finally:
            connection.close()
    except Exception:
        return reject(503, 'Operator service unavailable. Retry shortly.')
