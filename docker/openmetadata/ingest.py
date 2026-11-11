"""Run the upstream Postgres metadata workflow locally; keep credentials out of logs."""
import argparse
import contextlib
import io
import json
import logging
import os
import signal
import time

# Upstream handlers can capture stderr at import time; disable them before
# importing, rather than relying only on redirect_stderr during execution.
logging.disable(logging.CRITICAL)
from metadata.workflow.metadata import MetadataWorkflow


def configuration():
    token = os.environ["SCARLET_OM_INGESTION_TOKEN"]
    password = os.environ["SCARLET_SAMPLE_READER_PASSWORD"]
    if not token or not password:
        raise ValueError("missing local credentials")
    return {
        "source": {
            "type": "postgres",
            "serviceName": "ScarletPostgres",
            "serviceConnection": {"config": {
                "type": "Postgres", "username": "sample_reader",
                "authType": {"password": password},
                "hostPort": "sample-db:5432", "database": "scarlet_sample",
                "ingestAllDatabases": False, "sslMode": "disable",
            }},
            "sourceConfig": {"config": {
                "type": "DatabaseMetadata", "includeTables": True,
                "includeViews": True, "includeTags": True,
                "includeOwners": False, "includeStoredProcedures": False,
                "markDeletedTables": False,
                "schemaFilterPattern": {"includes": ["^inventory$"]},
                # This dedicated synthetic service follows source descriptions.
                # Scarlet's confirmed classification remains independently versioned.
                "overrideMetadata": True,
                "extractJsonSchema": False,
            }},
        },
        "sink": {"type": "metadata-rest", "config": {}},
        "workflowConfig": {
            "loggerLevel": "ERROR",
            "openMetadataServerConfig": {
                "hostPort": "http://openmetadata:8585/api",
                "authProvider": "openmetadata",
                "securityConfig": {"jwtToken": token},
            },
        },
    }


def collect():
    workflow = None
    # Upstream connector failures may include configuration. Only fixed status
    # metadata leaves this local process; source values and secrets stay local.
    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
        try:
            workflow = MetadataWorkflow.create(configuration())
            workflow.execute()
            workflow.raise_from_status()
        finally:
            if workflow is not None:
                workflow.stop()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--interval", type=int, default=300)
    args = parser.parse_args()
    if not 60 <= args.interval <= 86400:
        parser.error("interval must be between 60 and 86400 seconds")
    running = True

    def stop(*_):
        nonlocal running
        running = False

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    while running:
        success = False
        try:
            collect()
            success = True
        except Exception:
            pass
        print(json.dumps({"service": "ScarletPostgres", "database": "scarlet_sample", "schema": "inventory", "status": "success" if success else "error", "finishedAt": int(time.time()), "scope": "metadata_only_no_profile_or_raw_values"}), flush=True)
        if args.once:
            return 0 if success else 1
        deadline = time.monotonic() + args.interval
        while running and time.monotonic() < deadline:
            time.sleep(min(1, max(0, deadline - time.monotonic())))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
