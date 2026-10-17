# relaix-server

The Go control plane for [Relaix](https://github.com/KGMA74/relaix) — a self-hosted gateway
that turns ordinary Android phones into SMS and USSD nodes: send SMS, run USSD codes, read
the SMS the phones receive, and let automations on the phones act on them.

## Status

**Working end to end, single instance.** `gatewayd` serves the REST API (under `/api/v1`),
the gRPC stream the phones hold open, the operator dashboard (`/ui`) and an MCP server for
AI assistants (`/mcp`).

```sh
make run
```

## What this is

Phones sit behind carrier NAT and can never be reached inbound, so each one runs an agent
that dials out and holds a gRPC bidirectional stream open. This server accepts those
streams, tracks which devices are alive, schedules jobs onto them and reports back.

| Package | What it does |
| --- | --- |
| `api` | REST under `/api/v1`: SMS and USSD jobs, history, received SMS, devices and their rules, stats, live events (SSE), fine-grained key permissions. |
| `mcp` | Model Context Protocol server: the API as tools for an assistant, OAuth for claude.ai, tools an embedding program adds. |
| `library` | Ready-made automation templates with `{{params}}`, installed like plugins. |
| `policy` | Per-device and per-SIM rules (hours, countries, caps, forwarded SMS) and the automations run on the phone. |
| `hub` | Registry of connected devices, owned by one goroutine (actor pattern). |
| `scheduler` | Tick loop pairing pending jobs with ready devices: immediate or queued, chosen or automatic, priority, scheduled. |
| `grpcserver` | The `DeviceGateway` service: enrollment and the `Connect` stream. |
| `devices` | Enrollment tokens, one entry per handset, leaving the fleet. |
| `callback` | Signed webhooks (`{"event", "data"}`, HMAC-SHA256) with retries. |
| `events` | The live event broker behind `GET /events`. |
| `store`, `db` | Persistence interfaces, their Postgres implementation, the schema. |
| `gateway` | Wires it all; `Extensions` lets a program (the hosted cloud) add accounts, routes, a library and MCP tools. |
| `ui` | The dashboard (React), built into the binary. |

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
make db-up      # start postgres on :15432 and wait for it
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

## Author

Built by **[KGMA74](https://github.com/KGMA74)** — [kgmadev.com](https://kgmadev.com).

## License

[Apache License 2.0](LICENSE), same as the rest of Relaix.
