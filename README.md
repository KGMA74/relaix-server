# relaix-server

The Go control plane for [Relaix](https://github.com/KGMA74/relaix) — a self-hosted SMS
gateway whose sending nodes are ordinary Android phones.

## Status

**Early development — not yet functional.**

`gatewayd` builds and runs, but serves nothing yet: nothing is wired into the entry point,
so it starts, says so, and shuts down cleanly on a signal. The pieces themselves exist and
are tested — hub, scheduler, the gRPC `Connect` stream, the store interfaces, the schema —
they are simply not connected to `main` yet. That happens one component at a time.

```sh
make run
```

## What this is

Phones sit behind carrier NAT and can never be reached inbound, so each one runs an agent
that dials out and holds a gRPC bidirectional stream open. This server accepts those
streams, tracks which devices are alive, and schedules SMS jobs onto them:

| Package | What it does | State |
| --- | --- | --- |
| `hub` | Registry of connected devices, owned by one goroutine (actor pattern). | done |
| `scheduler` | Tick loop pairing pending jobs with ready devices: `immediate` vs `queued`, explicit or automatic selection, priority, `scheduledAt`. | done |
| `grpcserver` | The `DeviceGateway` service. `Connect` (bidi stream) done; `Enroll` next. | partial |
| `store` | Persistence interfaces. Postgres implementation still to come. | interfaces only |
| `db` | Schema and migrations. | done |
| `token` | Generation and hashing of device and enrollment tokens. | done |
| `api` | REST: job submission and inspection, device listing, enrollment tokens. | not started |
| `callback` | HMAC-signed status webhooks with exponential-backoff retry. | not started |

Design and rationale live in the monorepo:
[architecture](https://github.com/KGMA74/relaix/blob/main/docs/architecture.md) ·
[protocol](https://github.com/KGMA74/relaix/blob/main/docs/protocol.md).

## Development

```sh
make            # list every target
make check      # gofmt check + go vet + tests
make test-race  # tests under the race detector
```

`make test-race` runs in a `golang` container on purpose. The race detector needs cgo, and
the usual dev machine here is Windows without a C toolchain — so rather than silently not
running, it runs somewhere it works. For a concurrent component, "tests pass" without the
detector is not a meaningful claim.

A throwaway Postgres for migrations:

```sh
make db-up      # start postgres on :55432 and wait for it
make migrate    # apply db/migrations
make psql       # shell into it
make db-down    # remove it
```

## Docker

```sh
make docker-build
docker run --rm relaix-server:latest
```

Multi-stage, static binary on `distroless/static`: about 9 MB, no shell, running as
`nonroot`. `gatewayd` is PID 1 and receives `SIGTERM` directly, which is what its ordered
shutdown depends on.

## Generated code

`gen/` is generated from `proto/smsgateway/v1/device.proto`, which lives in the
[relaix monorepo](https://github.com/KGMA74/relaix) — the single source of truth for the
contract, shared with the Android agent.

The generated Go is **committed here on purpose**, so building this repository — or its
container image — needs nothing but the Go toolchain: no `buf`, no `protoc`, no plugin set
to keep in sync with CI.

To regenerate after a change to the proto, run `buf generate` from the monorepo root — its
`buf.gen.yaml` writes into this repository — then commit the result here alongside the
proto change, and bump the submodule pointer in the monorepo.

## License

[Apache License 2.0](LICENSE), same as the rest of Relaix.
