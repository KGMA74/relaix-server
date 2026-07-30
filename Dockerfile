# Build stage.
#
# The generated protobuf code is committed, so this needs nothing but the Go
# toolchain — no buf, no protoc, no plugin set to keep in sync with CI.
FROM golang:1.26 AS build

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module
# cache on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev

# CGO off so the result is a static binary that runs on a distroless base with
# no libc to match. Trimpath keeps build-machine paths out of panics.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/gatewayd ./cmd/gatewayd

# Runtime stage.
#
# Distroless rather than alpine: gatewayd talks gRPC and HTTP and touches no
# shell, so shipping one is attack surface with no upside. It also means CA
# certificates and /etc/passwd come from the base rather than being installed.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=build /out/gatewayd /gatewayd

# gRPC (devices) and HTTP (API). Documentation only — publishing is the
# operator's call.
EXPOSE 9090 8080

# Runs as uid 65532. The process binds no privileged port and writes no files,
# so there is nothing it needs root for.
USER nonroot:nonroot

# No shell in the image, so this is the exec form by necessity as well as by
# preference: gatewayd is PID 1 and receives SIGTERM directly, which is what
# its ordered shutdown depends on.
ENTRYPOINT ["/gatewayd"]
