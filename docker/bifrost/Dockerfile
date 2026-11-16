FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS scarlet-build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
COPY backend/go.mod backend/go.sum ./
COPY backend/pkg ./pkg
COPY backend/internal/dataformat ./internal/dataformat
COPY backend/internal/mediaprobe ./internal/mediaprobe
COPY backend/cmd/ai-gateway ./cmd/ai-gateway
RUN CGO_ENABLED=0 go build -trimpath -o /ai-gateway ./cmd/ai-gateway

# Official OSS transports/v2.5.1, source 7cb75fb2f8276ca48d0792c857fecb67b8879e2f.
FROM maximhq/bifrost:v2.5.1@sha256:5ec9873acaf1687556b6e4dd854b92b539d9debf87a5a683b66338648b84d3c8
LABEL io.scarlet.media-inspection-version="scarlet-media-ocr-asr-v2"
COPY --from=scarlet-build /ai-gateway /usr/local/bin/ai-gateway
# ai-gateway supervises /app/main, writes its restricted config in a private
# temporary directory and binds Bifrost only to loopback. No Bifrost port published.
ENTRYPOINT ["/usr/local/bin/ai-gateway"]
CMD ["-bifrost-binary","/app/main","-listen","0.0.0.0:8905","-catalog","/config/catalog.json","-bindings","/config/bindings.json","-bifrost-profile","/config/bifrost.json","-documents","/config/documents.json","-audit","/state/audit.jsonl","-journal","/state/replay.jsonl","-control-url","https://controller.internal","-control-token-file","/config/controller.token","-workspace","default","-tls-cert","/config/tls.crt","-tls-key","/config/tls.key"]
