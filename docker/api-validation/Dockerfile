FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -o /out/scarlet-api-validator ./cmd/api-validator && go build -trimpath -o /out/scarlet-api-validation-sample ./cmd/api-validation-sample
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
COPY --from=build /out/* /usr/local/bin/
RUN mkdir /state && chown 10001:10001 /state
USER 10001:10001
ENTRYPOINT ["scarlet-api-validator"]
