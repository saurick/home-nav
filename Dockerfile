FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal

RUN go build -o /out/home-nav ./cmd/home-nav

FROM alpine:3.22

WORKDIR /app
COPY --from=build /out/home-nav /usr/local/bin/home-nav
COPY config.example.yaml /app/services.yaml

EXPOSE 8080
ENTRYPOINT ["home-nav"]
CMD ["-addr", "0.0.0.0:8080", "-config", "/app/services.yaml"]
