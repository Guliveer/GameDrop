# GameDrop documentation

Start here. Documents are grouped by audience: operators, contributors, and
maintainers. All documentation is written in English.

## Operations

| Document | Contents |
|---|---|
| [Getting started](getting-started.md) | First run without technical steps: wizard, Telegram, first cycle, troubleshooting |
| [Configuration](configuration.md) | Every environment variable, defaults per mode, validation rules, example configurations |
| [Fly.io deployment](fly-deployment.md) | App/volume/Machine creation, image updates, secrets, logs, recovery, backups |
| [Operations](operations.md) | Log fields, run statuses, incident playbooks, monitoring, retention |

## Contributors

| Document | Contents |
|---|---|
| [Development](development.md) | Bootstrap, local run, quality gates, conventions, PR checklist |
| [Testing](testing.md) | Test layers, fixtures, fake clock, coverage, how to add tests |
| [Store adapters](stores.md) | Adapter contract, per-store endpoints and rules, adding a store |
| [Architecture](architecture.md) | Component map, cycle data flow, failure model, design decisions |

## Maintainers

| Document | Contents |
|---|---|
| [Database](database.md) | Schema, indexes, invariants, migrations, offer and outbox lifecycles, backup |
| [Notifications](notifications.md) | Rendering, outbox dedupe rules, retry policy, Telegram specifics |
| [Architecture Decision Records](adr/README.md) | Why the system is shaped the way it is |

## Quick reference

```bash
go run ./cmd/gamedrop --setup  # first-run wizard: .env, Telegram, first cycle
go run ./cmd/gamedrop          # one collection cycle
go test ./...                  # all tests (offline)
gofmt -l . && go vet ./...     # formatting and static checks
docker build -t gamedrop .     # production image
fly deploy --build-only --push --image-label <tag>   # image only, no Machines
```

Exit codes: `0` the cycle completed (all sources succeeded, or at least one
while others failed), `1` the cycle could not complete, `2` configuration is
invalid.
