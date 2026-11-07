# Build the MinIO Community Edition source checkout for the Docker target OS.
# The checked-out source requires Go 1.24 or newer.
FROM golang:1.24-alpine AS builder

ARG TARGETARCH
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=${TARGETARCH}

WORKDIR /src
COPY . .

# Follow MinIO's own build tag and produce a static Linux executable.
RUN go build -tags kqueue -trimpath -o /out/minio .

# Keep the runtime image small; certificates allow MinIO to make outbound TLS
# connections if needed. This is a local learning image, not a hardened release.
FROM alpine:3.21

RUN apk add --no-cache ca-certificates
COPY --from=builder /out/minio /usr/local/bin/minio

EXPOSE 9000 9001
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/minio"]
CMD ["server", "/data", "--console-address", ":9001"]
