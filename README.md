# relaix-server

The Go control plane for [Relaix](https://github.com/KGMA74/relaix) — a self-hosted SMS
gateway whose sending nodes are ordinary Android phones.

## Status

**Early development — not yet functional.**

This repository currently holds the generated protobuf contract and nothing else. The
control plane itself is being written one atomic commit at a time.

## What this is

Phones sit behind carrier NAT and can never be reached inbound, so each one runs an agent
that dials out and holds a gRPC bidirectional stream open. This server accepts those
streams, tracks which devices are alive, and schedules SMS jobs onto them:

- **hub** — a single goroutine owning the registry of connected devices (actor pattern).
- **scheduler** — a tick loop pairing pending jobs with ready devices, with `immediate`
  (fail-fast) and `queued` modes, explicit or automatic device selection, and priority.
- **gRPC server** — the `DeviceGateway` service: unary `Enroll`, bidirectional `Connect`.
- **REST API** — job submission and inspection, device listing, enrollment tokens.
- **callback watcher** — HMAC-signed status webhooks with exponential-backoff retry.

Design and rationale live in the monorepo:
[architecture](https://github.com/KGMA74/relaix/blob/main/docs/architecture.md) ·
[protocol](https://github.com/KGMA74/relaix/blob/main/docs/protocol.md).

## Generated code

`gen/` is generated from `proto/smsgateway/v1/device.proto`, which lives in the
[relaix monorepo](https://github.com/KGMA74/relaix) — the single source of truth for the
contract, shared with the Android agent.

The generated Go is **committed here on purpose**, so building this repository needs
nothing but the Go toolchain:

```sh
go build ./...
```

To regenerate after a change to the proto, run `buf generate` from the monorepo root — its
`buf.gen.yaml` writes into this repository — then commit the result alongside the proto
change.

## License

Not yet chosen.
