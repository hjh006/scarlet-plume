FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS speech
RUN apk add --no-cache cmake g++ git make
WORKDIR /src/whisper
RUN git init . \
    && git remote add origin https://github.com/ggml-org/whisper.cpp.git \
    && git fetch --depth=1 origin d1be6fde11ac6e0407606b4e42fe72d34add8037 \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = d1be6fde11ac6e0407606b4e42fe72d34add8037
RUN cmake -S . -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
    -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF -DWHISPER_BUILD_TESTS=OFF \
    -DWHISPER_BUILD_EXAMPLES=ON -DWHISPER_BUILD_SERVER=OFF \
    -DWHISPER_CURL=OFF -DWHISPER_BUILD_IS_DEV=OFF \
    && cmake --build build --target whisper-cli --parallel 2

FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS scanner
WORKDIR /src/backend
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
ARG GOPROXY=https://proxy.golang.org,direct
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN go build -trimpath -o /out/scarlet-media-inspector ./cmd/media-inspector

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates libstdc++ tesseract-ocr tesseract-ocr-data-eng
COPY --from=speech /src/whisper/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=scanner /out/scarlet-media-inspector /usr/local/bin/scarlet-media-inspector
USER 10001:10001
EXPOSE 8912
ENTRYPOINT ["/usr/local/bin/scarlet-media-inspector"]
