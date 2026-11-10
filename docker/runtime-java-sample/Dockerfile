FROM --platform=$BUILDPLATFORM eclipse-temurin:25-jdk-noble@sha256:95e350ad1eb738f943e141640f7e3d797e483359a3b33c3864255bfd1ad71f8a AS build
WORKDIR /opt/scarlet-runtime
# Java Agent 2.33.1 targets the OpenTelemetry Java SDK and API 1.67.0.
ADD --chmod=0444 --checksum=sha256:538d1dff2f9a37aecbc0ef98fd17273a3affb058e734e6763a3a07a38a5fdc59 https://repo.maven.apache.org/maven2/io/opentelemetry/opentelemetry-api/1.67.0/opentelemetry-api-1.67.0.jar lib/opentelemetry-api.jar
ADD --chmod=0444 --checksum=sha256:40fb813fb5e27ec2691daa29c81b572b5b1541ef9b41e62490c3ee3e6407c55e https://repo.maven.apache.org/maven2/io/opentelemetry/opentelemetry-context/1.67.0/opentelemetry-context-1.67.0.jar lib/opentelemetry-context.jar
ADD --chmod=0444 --checksum=sha256:da97bdb395418e16ecdcbe7ad3497f45b9e767ceda4ae7e86133e7a757ac377f https://repo.maven.apache.org/maven2/io/opentelemetry/opentelemetry-common/1.67.0/opentelemetry-common-1.67.0.jar lib/opentelemetry-common.jar
COPY Main.java .
RUN javac --add-modules jdk.httpserver -cp 'lib/*' -d classes Main.java

FROM eclipse-temurin:25-jre-noble@sha256:a98128a57de944a72bd31fbc79b898edd5a5864d3652071d5bbd2160ab7d7dce
WORKDIR /opt/scarlet-runtime
COPY --from=build /opt/scarlet-runtime/classes ./classes
COPY --from=build /opt/scarlet-runtime/lib ./lib
ADD --chmod=0444 --checksum=sha256:73914527305a40cce504b0d3d90b23caf565136912d607ae8ae7c5895512332c https://repo.maven.apache.org/maven2/org/postgresql/postgresql/42.7.14/postgresql-42.7.14.jar lib/postgresql.jar
ADD --chmod=0444 --checksum=sha256:e43a270df53c6f423e0ab157be9c27ccc01d5dc3232e162fe54777573db2f149 https://repo.maven.apache.org/maven2/io/opentelemetry/javaagent/opentelemetry-javaagent/2.33.1/opentelemetry-javaagent-2.33.1.jar agent.jar
ADD --chmod=0444 --checksum=sha256:857658c8bdcbff794949a8d9922f88d63ee3751c203c7cd7d21ea97f9e745288 https://repo.maven.apache.org/maven2/org/checkerframework/checker-qual/3.55.1/checker-qual-3.55.1.jar lib/checker-qual.jar

ENV OTEL_SERVICE_NAME=scarlet-java-sample \
    OTEL_TRACES_EXPORTER=otlp \
    OTEL_METRICS_EXPORTER=none \
    OTEL_LOGS_EXPORTER=none \
    OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf \
    OTEL_SEMCONV_STABILITY_OPT_IN=http,database \
    OTEL_BSP_MAX_EXPORT_BATCH_SIZE=128 \
    OTEL_BSP_SCHEDULE_DELAY=1500

EXPOSE 8080
USER 10001:10001
ENTRYPOINT ["java", "--add-modules", "jdk.httpserver", "-javaagent:/opt/scarlet-runtime/agent.jar", "-cp", "classes:lib/*", "Main"]
