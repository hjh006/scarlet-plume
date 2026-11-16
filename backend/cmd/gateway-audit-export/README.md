# Verified gateway audit archive

Build with `go build ./cmd/gateway-audit-export` from `backend`.
Set `SCARLET_GATEWAY_CONTROL_TOKEN` to the node token bound to this workspace;
pass secrets through the environment, never flags or URLs.

```sh
./gateway-audit-export \
  -platform https://control.example.com \
  -workspace enterprise \
  -output /private/scarlet/audit.jsonl \
  -follow
```

The default checkpoint is `audit.jsonl.checkpoint`. Existing archive and checkpoint
files must be private regular files; new files use mode 0600 and directories 0700.
One process holds an exclusive archive lock. Do not rotate or truncate an active
archive; stop the exporter and select a new archive/checkpoint pair for rotation. Each page is checked against the
workspace, sequence, previous hash, event hashes and current server head. The
archive is fsynced before an atomic, fsynced checkpoint replacement. After a crash,
complete verified newline records recover a stale checkpoint; a partial last line
is truncated. A checkpoint ahead of its archive or a broken chain stops export.

`-follow` polls every 15 seconds and retries network/429/5xx failures without
advancing the checkpoint. Invalid chains, authentication errors and disk errors
stop the process. Omit `-follow` for one catch-up run. `-local` allows loopback HTTP
for development only. Retain both files together and point an existing log shipper
at the JSONL archive for external retention. The CLI does not provide a native
S3/GCS writer or an external trusted hash anchor; database-owner rewriting and
whole-archive replacement require a separately retained trusted checkpoint to detect.
