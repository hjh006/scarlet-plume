FROM node:26-alpine@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216 AS frontend
WORKDIR /src/web
RUN apk add --no-cache python3
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/index.html web/tsconfig.json web/vite.config.ts ./
COPY web/src/ ./src/
COPY extension/browser-field-labeler/ /src/extension/browser-field-labeler/
COPY governance/static/ /src/governance/static/
RUN npm run build

FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS backend
WORKDIR /src/backend
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY endpoint/windows-enterprise/ /src/endpoint/windows-enterprise/
RUN go build -trimpath -o /out/scarlet-server ./cmd/server \
    && go build -trimpath -o /out/scarlet-agent ./cmd/agent \
    && go build -trimpath -o /out/scarlet-runtime-agent ./cmd/runtime-agent \
    && go build -trimpath -o /out/scarlet-gateway-log ./cmd/gateway-log \
    && go build -trimpath -o /out/scarlet-discovery-agent ./cmd/discovery-agent \
    && go build -trimpath -o /out/scarlet-value-fingerprint ./cmd/value-fingerprint \
    && go build -trimpath -o /out/scarlet-classification-delivery-agent ./cmd/classification-delivery-agent \
    && go build -trimpath -o /out/scarlet-application-agent ./cmd/application-agent \
    && go build -trimpath -o /out/scarlet-control-agent ./cmd/control-agent \
    && go build -trimpath -o /out/scarlet-event-bridge ./cmd/event-bridge
RUN go build -trimpath -o /out/scarlet-data-transfer-guard ./cmd/data-transfer-guard
RUN go build -trimpath -o /out/scarlet-endpoint-agent ./cmd/endpoint-agent \
    && GOOS=windows GOARCH=amd64 go build -trimpath -o /downloads/scarlet-endpoint-agent-windows-amd64.exe ./cmd/endpoint-agent \
    && GOOS=windows GOARCH=arm64 go build -trimpath -o /downloads/scarlet-endpoint-agent-windows-arm64.exe ./cmd/endpoint-agent \
    && go run ./cmd/endpoint-package -source /src/endpoint/windows-enterprise -binaries /downloads -out /downloads/windows-enterprise-deployment.zip

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
WORKDIR /app
COPY --from=backend /out/ /usr/local/bin/
COPY --from=frontend /src/web/dist/ /app/web/
COPY --from=backend /downloads/ /app/web/downloads/
COPY docs/endpoint-channel-enforcement.md docs/windows-native-endpoint-observation.md docs/windows-enterprise-native-controls.md docs/windows-clipboard-session.md /app/web/downloads/
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["scarlet-server"]
