FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/home-nav ./cmd/home-nav

FROM alpine:3.22

WORKDIR /app
COPY --from=build /out/home-nav /usr/local/bin/home-nav
COPY config.example.yaml /app/services.yaml

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["home-nav"]
CMD ["-addr", "0.0.0.0:8080", "-config", "/app/services.yaml"]
