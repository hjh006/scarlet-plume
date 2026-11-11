"""Explicitly synthetic HTTP export and source-side periodic measurement."""
import datetime
import json
import os
from pathlib import Path
import secrets
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.request import ProxyHandler, build_opener

MODE = Path('/data/mode')


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == '/healthz':
            self.send_response(204)
            self.end_headers()
        elif self.path == '/api/export':
            try:
                enabled = MODE.read_text().strip() == 'public'
            except OSError:
                enabled = False
            if not enabled:
                self.send_response(403)
                self.end_headers()
                return
            body = json.dumps({'synthetic': True, 'scope': 'scarlet-wazuh-export', 'rows': [{'email': 'wazuh-demo@example.invalid'}]}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.send_header('Cache-Control', 'no-store')
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, *args):
        pass


def measure():
    opener = build_opener(ProxyHandler({}))
    def get(path):
        try:
            with opener.open('http://wazuh-export-sample:8080' + path, timeout=3) as response:
                return response.status, response.read(8193)
        except HTTPError as error:
            return error.code, b''
        except OSError:
            return 0, b''
    health, _ = get('/healthz')
    status, body = get('/api/export')
    result = 'unknown'
    if health == 204:
        if status == 403:
            result = 'cleared'
        elif status == 200:
            try:
                value = json.loads(body)
                if value.get('synthetic') is True and value.get('scope') == 'scarlet-wazuh-export' and value.get('rows') == [{'email': 'wazuh-demo@example.invalid'}]:
                    result = 'observed'
            except (ValueError, AttributeError):
                pass
    return {'scarlet_check': 'public_export_v1', 'scope': 'scarlet-wazuh-export', 'result': result, 'export_status': str(status), 'health_status': str(health), 'proof': 'confirmed' if result != 'unknown' else 'inconclusive', 'measurement_id': secrets.token_hex(16), 'measured_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}


def main():
    if sys.argv[1:] in (['check'], ['check-once']):
        log = Path('/logs/checks.json')
        previous = None
        while True:
            try:
                measured = measure()
                if sys.argv[1:] == ['check-once'] or measured['result'] != previous:
                    with log.open('a', encoding='utf8') as stream:
                        stream.write(json.dumps(measured, separators=(',', ':')) + '\n')
                        stream.flush()
                        os.fsync(stream.fileno())
                previous = measured['result']
            except OSError:
                print('source measurement could not be persisted', flush=True)
            if sys.argv[1:] == ['check-once']:
                return
            time.sleep(15)
    else:
        MODE.parent.mkdir(parents=True, exist_ok=True)
        if not MODE.exists():
            MODE.write_text('private\n')
        ThreadingHTTPServer(('0.0.0.0', 8080), Handler).serve_forever()


if __name__ == '__main__':
    main()
