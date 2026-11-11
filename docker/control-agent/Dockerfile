FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -o /out/scarlet-control-agent ./cmd/control-agent
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN mkdir /data && chown 10001:10001 /data
COPY --from=build /out/scarlet-control-agent /usr/local/bin/
USER 10001:10001
ENTRYPOINT ["scarlet-control-agent"]
