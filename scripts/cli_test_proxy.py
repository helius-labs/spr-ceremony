"""Loopback-only integration-test proxy with controllable claim interruptions."""
import http.client
import http.server
import ssl
import threading


class Proxy:
    def __init__(self, upstream, certificate=None, key=None):
        self.block_after_first_claim = set()
        self.claims = {}
        proxy = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def forward(self):
                token = self.headers.get('Authorization', '').removeprefix('Bearer ')
                if self.path == '/v1/claim' and token in proxy.block_after_first_claim:
                    proxy.claims[token] = proxy.claims.get(token, 0) + 1
                    if proxy.claims[token] > 1:
                        self.send_error(503, 'Test interruption')
                        return
                length = int(self.headers.get('Content-Length', '0'))
                if length > 16 << 20:
                    self.send_error(413)
                    return
                body = self.rfile.read(length) if length else None
                connection = http.client.HTTPConnection('127.0.0.1', upstream, timeout=60)
                try:
                    headers = {k: v for k, v in self.headers.items() if k.lower() not in ['connection', 'transfer-encoding']}
                    connection.request(self.command, self.path, body, headers)
                    response = connection.getresponse()
                    payload = response.read()
                    self.send_response(response.status)
                    for k, v in response.getheaders():
                        if k.lower() not in ['connection', 'transfer-encoding', 'content-length']:
                            self.send_header(k, v)
                    self.send_header('Content-Length', str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                except OSError:
                    self.send_error(502)
                finally:
                    connection.close()

            do_GET = do_POST = do_DELETE = forward

        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = True
        if certificate:
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(certificate, key)
            self.server.socket = context.wrap_socket(self.server.socket, server_side=True)
        self.origin = ('https' if certificate else 'http') + '://127.0.0.1:' + str(self.server.server_port)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
