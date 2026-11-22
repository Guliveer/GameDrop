# Fly.io deployment runbook

GameDrop runs as **one Scheduled Machine** with **one Fly Volume**. The Machine
starts on a schedule, runs one collection cycle, and exits.

## Why `fly deploy` is not the deployment mechanism

Fly has two different Machine lifecycles:

| | `fly deploy` (Fly Launch) | `fly machine run --schedule` |
|---|---|---|
| Managed by | fly.toml, Fly Launch | you, explicitly |
| `fly deploy` updates it | yes | **no** (ignored by Fly Launch) |
| Starts | always-running or on demand | on the schedule only |
| Volume | from `[[mounts]]` | attached once with `--volume` |

A Scheduled Machine created with `fly machine run --schedule` is intentionally
invisible to `fly deploy`. Running `fly deploy` against this app would create or
update a *different* Machine (an always-running one, since the process is a
one-shot worker) and leave the scheduled Machine on the old image.

So the flow is:

1. build and push an image with `fly deploy --build-only --push`;
2. point the existing Scheduled Machine at it with `fly machine update`.

`fly machine update` composes the new configuration from the Machine's existing
config plus the flags you pass, so the schedule, region, environment, and
attached volume are preserved. `--skip-start` stops flyctl from launching the
Machine immediately; the next scheduled run uses the new image.

## First-time setup

Everything below is manual and requires your Fly credentials. Nothing in this
repository creates or deploys infrastructure.

```bash
# 0. Authenticate
fly auth login

# 1. Create the app (name must match fly.toml or pass --app)
fly apps create gamedrop

# 2. Create the persistent volume in the same region as the Machine.
#    SQLite lives here and must survive machine replacements.
fly volumes create gamedrop_data --region fra --size 1 --app gamedrop

# 3. Store secrets. These never go into fly.toml or the image.
fly secrets set --app gamedrop \
  TELEGRAM_BOT_TOKEN=123456:replace-me \
  TELEGRAM_CHAT_ID=987654321

# 4. Build and push the image without touching Machines.
fly deploy --app gamedrop --build-only --push --image-label bootstrap

# 5. Create the Scheduled Machine.
#    --schedule hourly       -> one run per approximate hour
#    --restart on-failure    -> a failed cycle is retried by Fly
#    --volume                -> SQLite on the persistent volume
#    Do NOT pass --rm: it destroys the Machine on exit, including the schedule.
fly machine run registry.fly.io/gamedrop:bootstrap \
  --app gamedrop \
  --region fra \
  --schedule hourly \
  --restart on-failure \
  --volume gamedrop_data:/data \
  --vm-size shared-cpu-1x \
  --vm-memory 256 \
  --env APP_MODE=production \
  --env DATABASE_PATH=/data/offers.db \
  --env STORE_COUNTRIES=PL \
  --env STORE_LOCALE=pl-PL

# 6. Note the Machine ID; GitHub Actions needs it as a repository variable.
fly machine list --app gamedrop
```

`fly machine run` starts the Machine once immediately, then Fly restarts it on
the approximate schedule.

## Updating the image

```bash
# 1. Build and push (no Machine changes)
fly deploy --app gamedrop --build-only --push --image-label "$(git rev-parse --short HEAD)"

# 2. Point the scheduled Machine at the new image, keeping everything else
fly machine update <machine-id> --app gamedrop \
  --image registry.fly.io/gamedrop:$(git rev-parse --short HEAD) \
  --skip-start --yes

# 3. Verify
fly machine list --app gamedrop
```

The volume is untouched by both commands. Never run `fly volumes destroy` or
detach the volume as part of a deploy; SQLite is the only copy of the data.

## Logs

```bash
fly logs --app gamedrop            # live tail (scheduled runs appear as they happen)
fly logs --app gamedrop --no-tail  # recent lines
```

Each run logs a start line, per-source outcomes, and one `collection cycle
finished` summary with the run ID and counters. Production logs are JSON.

## Inspecting a run and the database

```bash
# Machine status (started/stopped/suspended and the schedule)
fly machine status <machine-id> --app gamedrop

# A shell is not possible in the distroless image. Inspect SQLite locally:
fly machine list --app gamedrop
# copy the database out of the volume with a temporary helper machine:
fly machine run --app gamedrop --rm -it \
  --volume gamedrop_data:/data alpine:3.21 \
  sh -c 'apk add --no-cache sqlite && sqlite3 /data/offers.db "select store,country,offer_id,title,availability from offers limit 20"'
```

Prefer running that helper at a time when no scheduled cycle is expected, and
never write to the database from the helper.

## Recovery

| Symptom | Action |
|---|---|
| Machine stopped and no new runs | `fly machine list`; if the Machine was destroyed, recreate it with step 5 (the volume and its data are separate) |
| Every cycle exits 1 | `fly logs`; a run fails when the database cannot be opened or no source succeeded. Check secrets and Fly status |
| Database file missing | Confirm the volume is attached: `fly machine status <id>`. The app creates the directory but cannot recreate lost data |
| Telegram delivery failing | Notifications stay queued in the `notifications` table and are retried with backoff up to `NOTIFY_MAX_ATTEMPTS`. Fix the token/chat ID and the queue drains on later runs |
| Machine scheduled but never runs | Scheduled starts are approximate and require host capacity; Fly does not retry a missed schedule automatically |

## Backups

The volume is a single copy. Periodically copy `offers.db` (with its `-wal`
sidecar) off the volume, for example with the helper Machine above and
`fly sftp get` or a `tar` to another location. SQLite is small (a few MB).

## Limitations of the single-Machine model

- A Fly Volume is bound to one Machine and one host; there is no replication.
  Host loss means downtime until Fly restarts the Machine elsewhere, and the
  volume cannot follow.
- Scheduled starts are approximate; there is no built-in alerting for missed or
  failed runs. Watch `fly logs` or add external monitoring.
- Two Machines sharing one SQLite file is not supported. Do not scale this
  worker horizontally.

## GitHub Actions deployment

`.github/workflows/deploy.yml` runs the full test suite and image build, then
pushes the image and, when `FLY_MACHINE_ID` is set, updates the Scheduled Machine:

- Repository secret: `FLY_API_TOKEN` (`fly tokens create deploy -a gamedrop`)
- Repository variable: `FLY_APP_NAME` (optional, defaults to `gamedrop`)
- Repository variable: `FLY_MACHINE_ID` (the scheduled Machine ID)

Without `FLY_MACHINE_ID` the workflow still pushes the image and prints a notice
telling you the exact `fly machine update` command. That is deliberate: updating
the wrong Machine (or creating an always-running one) is worse than a manual
step.
