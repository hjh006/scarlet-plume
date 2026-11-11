import copy
import json
from pathlib import Path
import tempfile
import unittest

from project import project, spool


class NativeProjectionTests(unittest.TestCase):
    def alert(self, safe=False):
        return {'id': '1794404100.42', 'timestamp': '2026-11-11T14:00:00.123+0000', 'agent': {'id': '000', 'name': 'private-agent-name'},
                'rule': {'id': '100502' if safe else '100501', 'level': 3 if safe else 10, 'description': 'private-description'},
                'location': '/scarlet-input/checks.json', 'full_log': 'private-source-original', 'data': {
                    'scarlet_check': 'public_export_v1', 'scope': 'scarlet-wazuh-export', 'result': 'cleared' if safe else 'observed',
                    'health_status': '204', 'export_status': '403' if safe else '200', 'proof': 'confirmed',
                    'measurement_id': 'a'*32, 'measured_at': '2026-11-11T14:00:00.100+00:00', 'password': 'private-secret'}}

    def test_native_pair_same_scope_and_only_allowlisted_metadata(self):
        positive, negative = project(self.alert()), project(self.alert(True))
        for event in [positive, negative]:
            self.assertEqual(event['eventId'], '1794404100.42')
            self.assertEqual(event['rule'], 'wazuh:scarlet.public_export.v1')
            self.assertNotIn('private-', json.dumps(event))
            self.assertNotIn('measurement_id', event)
        self.assertEqual((positive['outcome'], positive['count']), ('observed', 1))
        self.assertEqual((negative['outcome'], negative['count']), ('cleared', 0))

    def test_unobserved_or_wrong_scope_does_not_clear(self):
        unknown = self.alert(True)
        unknown['rule']['id'] = '100503'
        self.assertIsNone(project(unknown))
        unknown['rule']['id'] = '100599'
        self.assertIsNone(project(unknown))
        wrong_location = self.alert(True)
        wrong_location['location'] = '/other/logs.json'
        with self.assertRaises(ValueError):
            project(wrong_location)
        for path, value in [(('data', 'health_status'), '0'), (('data', 'export_status'), '503'), (('data', 'proof'), 'inconclusive'),
                            (('data', 'scope'), 'other-system'), (('agent', 'id'), '001'), (('rule', 'level'), 10)]:
            alert = self.alert(True)
            alert[path[0]][path[1]] = value
            with self.assertRaises(ValueError):
                project(alert)
        alert = self.alert(True)
        alert['data']['measured_at'] = '2026-11-11T13:00:00+00:00'
        with self.assertRaises(ValueError):
            project(alert)

    def test_analysis_queue_cannot_refresh_an_old_measurement(self):
        alert = self.alert(True)
        alert['timestamp'] = '2026-11-11T14:01:00.100+0000'
        event = project(alert)
        self.assertEqual(event['observedAt'], '2026-11-11T14:00:00.100000+00:00')
        alert['timestamp'] = '2026-11-11T14:00:00.098+0000'
        with self.assertRaises(ValueError):
            project(alert)
        alert['timestamp'] = '2026-11-11T14:02:00.101+0000'
        with self.assertRaises(ValueError):
            project(alert)

    def test_spool_is_private_atomic_and_stable_for_native_replay(self):
        with tempfile.TemporaryDirectory() as directory:
            event = project(self.alert())
            spool(event, directory)
            spool(event, directory)
            files = list(Path(directory).glob('*.json'))
            self.assertEqual(len(files), 1)
            self.assertEqual(files[0].stat().st_mode & 0o777, 0o600)
            self.assertEqual(json.loads(files[0].read_text()), event)
            mutated = copy.deepcopy(event)
            mutated['count'] = 2
            with self.assertRaises(ValueError):
                spool(mutated, directory)
            self.assertEqual(json.loads(files[0].read_text()), event)


if __name__ == '__main__':
    unittest.main()
