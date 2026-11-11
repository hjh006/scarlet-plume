"""At-least-once metadata spool delivery using the existing Go event bridge."""
import datetime
import json
import os
from pathlib import Path
import subprocess
import time

PENDING = Path('/scarlet-spool/pending')
STATE = Path('/scarlet-spool/delivery-state.json')


def main():
    while True:
        try:
            files = sorted(PENDING.glob('*.json'), key=lambda path: (datetime.datetime.fromisoformat(json.loads(path.read_bytes())['observedAt']), path.name))[:50]
            for path in files:
                body = path.read_bytes()
                if len(body) > 16384:
                    raise ValueError('projection exceeds platform metadata limit')
                event = json.loads(body)
                # A first healthy measurement is not evidence of remediating an
                # observed risk. Keep no fabricated cleared case in the platform.
                state = json.loads(STATE.read_text()) if STATE.exists() else {'observed': False}
                if event['outcome'] == 'cleared' and not state['observed']:
                    path.unlink()
                    continue
                done = subprocess.run(['scarlet-event-bridge', '-url', 'http://app:8080', '-local', '-format', 'normalized'], input=body,
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=35, check=False)
                if done.returncode != 0:
                    print('platform delivery pending; metadata retained', flush=True)
                    break
                if event['outcome'] == 'observed' and not state['observed']:
                    temporary = STATE.with_suffix('.tmp')
                    with temporary.open('w') as stream:
                        json.dump({'observed': True}, stream)
                        stream.flush()
                        os.fsync(stream.fileno())
                    temporary.replace(STATE)
                    directory = os.open(STATE.parent, os.O_DIRECTORY)
                    try:
                        os.fsync(directory)
                    finally:
                        os.close(directory)
                path.unlink()
        except (OSError, ValueError, KeyError, subprocess.TimeoutExpired):
            print('source queue or platform unavailable; pending metadata retained', flush=True)
        time.sleep(2)


if __name__ == '__main__':
    main()
