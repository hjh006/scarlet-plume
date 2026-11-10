import os
import re
import signal
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import SpanKind


ORDER_PATH = re.compile(r"/runtime/python/orders/[0-9]{1,24}")
ORDER_ROUTE = "/runtime/python/orders/{id}"
LEGACY_ROUTE = "/runtime/python/legacy"
LEGACY_STATUS = {"200": 200, "410": 410}[os.getenv("SCARLET_RUNTIME_DEMO_LEGACY_STATUS", "410")]


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.partition("?")[0]
        if path == "/readyz":
            self.reply(200, b"ready\n")
            return
        if ORDER_PATH.fullmatch(path):
            route, status, body = ORDER_ROUTE, 200, b"synthetic order check complete\n"
        elif path == LEGACY_ROUTE:
            route, status, body = LEGACY_ROUTE, LEGACY_STATUS, b"synthetic legacy route\n"
        else:
            self.reply(404, b"not found\n")
            return

        # Export the fixed route template and status only.
        with self.tracer.start_as_current_span(
            "GET " + route,
            kind=SpanKind.SERVER,
            attributes={
                "http.request.method": "GET",
                "http.route": route,
                "http.response.status_code": status,
            },
            record_exception=False,
            set_status_on_exception=False,
        ):
            self.reply(status, body)

    def reply(self, status, body):
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass


class DemoServer(ThreadingHTTPServer):
    def handle_error(self, request, client_address):
        print("Python runtime demo could not complete a request.", file=sys.stderr)


def stop(*_):
    raise SystemExit(0)


if __name__ == "__main__":
    service = os.getenv("OTEL_SERVICE_NAME", "scarlet-python-sample")
    provider = TracerProvider(resource=Resource({"service.name": service}))
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    Handler.tracer = provider.get_tracer("scarlet.runtime.python.sample")
    signal.signal(signal.SIGTERM, stop)
    try:
        with DemoServer(("0.0.0.0", 8080), Handler) as server:
            server.serve_forever()
    finally:
        provider.shutdown()
