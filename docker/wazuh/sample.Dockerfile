FROM python:3.14-slim-bookworm@sha256:3fe1a0b6c2d6b2ebb572906cdabeca75aeb558c0c17db5ffefbcf8094871c32b
WORKDIR /opt/scarlet-wazuh
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
COPY sample.py ./
RUN mkdir /data /logs && chown 10001:10001 /data /logs
USER 10001:10001
ENTRYPOINT ["python", "/opt/scarlet-wazuh/sample.py"]
