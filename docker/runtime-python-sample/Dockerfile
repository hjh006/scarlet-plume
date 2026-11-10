FROM python:3.14-slim-bookworm@sha256:3fe1a0b6c2d6b2ebb572906cdabeca75aeb558c0c17db5ffefbcf8094871c32b

WORKDIR /opt/scarlet-runtime
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    OTEL_SERVICE_NAME=scarlet-python-sample \
    OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf \
    OTEL_EXPORTER_OTLP_TRACES_TIMEOUT=5 \
    OTEL_BSP_MAX_EXPORT_BATCH_SIZE=128 \
    OTEL_BSP_SCHEDULE_DELAY=1500

COPY requirements.txt .
RUN pip install --disable-pip-version-check --no-cache-dir -r requirements.txt
COPY sample.py .

EXPOSE 8080
USER 10001:10001
ENTRYPOINT ["python", "sample.py"]
