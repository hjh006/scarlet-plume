"""Source-only projection of a fixed, installed Wazuh custom-rule pair."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile

RULES = {'100501': ('observed', '200', 10), '100502': ('cleared', '403', 3)}


def parse_time(value):
    if not isinstance(value, str):
        raise ValueError('invalid timestamp type')
    value = re.sub(r'([+-][0-9]{2})([0-9]{2})$', r'\1:\2', value.replace('Z', '+00:00'))
    return datetime.datetime.fromisoformat(value)


def project(alert):
    if not isinstance(alert, dict) or not isinstance(alert.get('rule'), dict):
        raise ValueError('invalid native alert structure')
    native = str(alert.get('rule', {}).get('id', ''))
    if native not in RULES:
        return None
    result, status, level = RULES[native]
    data = alert.get('data', {})
    if not isinstance(data, dict) or not isinstance(alert.get('agent'), dict):
        raise ValueError('invalid source proof structure')
    if (alert.get('location') != '/scarlet-input/checks.json' or alert.get('agent', {}).get('id') != '000'
            or alert.get('rule', {}).get('level') != level or data.get('scarlet_check') != 'public_export_v1'
            or data.get('scope') != 'scarlet-wazuh-export' or data.get('result') != result
            or data.get('health_status') != '204' or data.get('export_status') != status
            or data.get('proof') != 'confirmed' or not re.fullmatch('[a-f0-9]{32}', data.get('measurement_id', ''))):
        raise ValueError('unexpected source proof or installed rule scope')
    event_id = alert.get('id', '')
    if not re.fullmatch('[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}', event_id):
        raise ValueError('invalid native alert ID')
    observed = parse_time(alert['timestamp'])
    measured = parse_time(data['measured_at'])
    if observed.tzinfo is None or measured.tzinfo is None or (observed-measured).total_seconds() > 120 or measured > observed + datetime.timedelta(milliseconds=1):
        raise ValueError('invalid source measurement timestamp')
    return {'eventId': event_id, 'observedAt': measured.astimezone(datetime.timezone.utc).isoformat(), 'outcome': result,
            'category': 'config', 'rule': 'wazuh:scarlet.public_export.v1', 'severity': 'high', 'service': 'Scarlet Wazuh Export',
            'method': 'GET', 'route': '/api/export', 'count': 1 if result == 'observed' else 0, 'windowSeconds': 15}


def spool(event, directory):
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    body = json.dumps(event, separators=(',', ':'), sort_keys=True).encode()
    target = directory / (hashlib.sha256(event['eventId'].encode()).hexdigest() + '.json')
    if target.exists() and target.read_bytes() != body:
        raise ValueError('event identity content conflict')
    fd, path = tempfile.mkstemp(prefix='.intake-', dir=directory)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(body)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(path, target)
        parent = os.open(directory, os.O_DIRECTORY)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
    finally:
        if os.path.exists(path):
            os.unlink(path)


def main():
    if len(sys.argv) != 2:
        raise ValueError('one native alert file required')
    raw = Path(sys.argv[1]).read_bytes()
    if len(raw) > 1 << 20:
        raise ValueError('native alert too large')
    event = project(json.loads(raw))
    if event is not None:
        spool(event, '/scarlet-spool/pending')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError):
        print('Scarlet source projection failed; retain native Wazuh alert for replay', file=sys.stderr)
        sys.exit(1)
