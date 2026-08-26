FROM golang:1.26.2-alpine3.23 AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /out/webhook-workbench ./cmd/webhook-workbench

FROM alpine:3.23.3

RUN addgroup -S -g 10001 workbench && \
    adduser -S -D -H -u 10001 -G workbench workbench && \
    mkdir -p /app/data && chown -R workbench:workbench /app

WORKDIR /app
COPY --from=build --chown=workbench:workbench /out/webhook-workbench /usr/local/bin/webhook-workbench

USER 10001:10001
EXPOSE 8080
ENV WEBHOOK_WORKBENCH_LISTEN=0.0.0.0:8080 \
    WEBHOOK_WORKBENCH_DATA=/app/data/events.json

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["webhook-workbench"]
