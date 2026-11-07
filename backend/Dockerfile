FROM golang:1.27.3-bookworm AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=secret,id=build_ca SSL_CERT_FILE=/run/secrets/build_ca go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /ledger .
FROM debian:bookworm-slim
RUN mkdir -p /data/attachments && chown 10001:10001 /data/attachments
COPY --from=build /ledger /ledger
USER 10001:10001
ENV LISTEN_ADDR=0.0.0.0:8080 STORAGE_DIR=/data/attachments
EXPOSE 8080
ENTRYPOINT ["/ledger"]
