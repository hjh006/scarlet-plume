FROM node:26-alpine@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216 AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/index.html web/tsconfig.json web/vite.config.ts ./
COPY web/src/ ./src/
COPY governance/static/ /src/governance/static/
RUN npm run build

FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS backend
WORKDIR /src/backend
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN go build -trimpath -o /out/scarlet-server ./cmd/server \
    && go build -trimpath -o /out/scarlet-agent ./cmd/agent \
    && go build -trimpath -o /out/scarlet-runtime-agent ./cmd/runtime-agent \
    && go build -trimpath -o /out/scarlet-gateway-log ./cmd/gateway-log \
    && go build -trimpath -o /out/scarlet-discovery-agent ./cmd/discovery-agent \
    && go build -trimpath -o /out/scarlet-control-agent ./cmd/control-agent \
    && go build -trimpath -o /out/scarlet-event-bridge ./cmd/event-bridge

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
WORKDIR /app
COPY --from=backend /out/ /usr/local/bin/
COPY --from=frontend /src/web/dist/ /app/web/
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["scarlet-server"]
