#!/usr/bin/env python3
"""Synthetic, loopback-only configuration API for isolated integration acceptance.

SCARLET_SAMPLE_ASSET_ID must be a real asset ID in the isolated fixture workspace.
SCARLET_SAMPLE_API_TOKEN is optional and stays in the process environment.
No customer records, prompts or vendor credentials are served.
"""
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

asset_id = int(os.environ["SCARLET_SAMPLE_ASSET_ID"])
if asset_id <= 0:
    raise SystemExit("configure a positive isolated fixture asset ID")
api_token = os.environ.get("SCARLET_SAMPLE_API_TOKEN", "")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass  # Do not log request paths, headers or credentials.

    def do_GET(self):
        if self.path == "/healthz":
            self.respond(200, {"status": "ok", "synthetic": True})
            return
        if self.path != "/configuration":
            self.respond(404, {"error": "unknown fixture route"})
            return
        supplied = self.headers.get("Authorization", "")
        if api_token and not hmac.compare_digest(supplied, "Bearer " + api_token):
            self.respond(401, {"error": "fixture authorization required"})
            return
        self.respond(200, {"items": [{"assetId": asset_id, "facts": {
            "application_name": "Synthetic support assistant",
            "application_vendor": "fixture-vendor",
            "application_model": "fixture-model",
            "application_region": "CN",
            "application_retention": "30 days configured",
            "application_training_use": "disabled",
            "application_service_audience": "internal",
            "application_knowledge_bases": "synthetic support documents",
            "application_tools": "read-only synthetic ticket lookup",
            "application_data_categories": "synthetic support metadata"
        }}]})

    def do_POST(self):
        self.respond(405, {"error": "read-only fixture"})

    do_PUT = do_POST
    do_PATCH = do_POST
    do_DELETE = do_POST

    def respond(self, status, value):
        body = json.dumps(value, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


server = ThreadingHTTPServer(("127.0.0.1", int(os.environ.get("SCARLET_SAMPLE_API_PORT", "0"))), Handler)
print(json.dumps({"url": f"http://127.0.0.1:{server.server_port}/configuration", "synthetic": True}), flush=True)
try:
    server.serve_forever()
except KeyboardInterrupt:
    pass
finally:
    server.server_close()
