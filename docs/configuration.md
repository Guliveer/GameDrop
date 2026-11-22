# Configuration

All configuration comes from environment variables. At startup GameDrop also
loads an optional `.env` file (see [Environment file](#environment-file) below)
without any third-party dependency. Invalid configuration is reported as one
aggregated error and the process exits with code `2` before opening the
database.

Booleans accept `1/true/yes/on` and `0/false/no/off` (case-insensitive).
Durations accept Go syntax (`30s`, `1500ms`) or a bare number of seconds.

## Reference

| Variable | Default | Validation | Notes |
|---|---|---|---|
| `ENV_FILE_PATH` | `.env` | must exist when set explicitly | Where the environment file is read from |
| `APP_MODE` | `development` | `development` \| `production` | Selects log format and the default database path |
| `LOG_LEVEL` | `info` | `debug`\|`info`\|`warn`\|`error` | |
| `DATABASE_PATH` | `gamedrop.db` (dev), `/data/offers.db` (prod) | non-empty | Parent directories are created |
| `HTTP_TIMEOUT_SEC` | `30s` | > 0 | Per-request timeout |
| `HTTP_MAX_RETRIES` | `2` | ≥ 0 | Retries for idempotent requests only |
| `HTTP_USER_AGENT` | `GameDrop/1.0 (+https://github.com/Guliveer/GameDrop)` | — | Sent to stores and Telegram |
| `STORE_COUNTRIES` | `PL` | ≥ 1, each 2 letters | Comma separated, uppercased, de-duplicated |
| `STORE_LOCALE` | `pl-PL` | non-empty | Mapped per store (Steam language, Epic locale, GOG locale) |
| `ENABLE_STEAM` | `true` | — | |
| `ENABLE_EPIC` | `true` | — | |
| `ENABLE_GOG` | `true` | — | |
| `ENABLE_PRIME` | `false` | — | Disabled adapter; enabling records a non-fatal failure |
| `SOURCE_TIMEOUT_SEC` | `45s` | > 0 | One store/country fetch, including verification calls |
| `MAX_CONCURRENCY` | `4` | ≥ 1 | Parallel store/country fetches |
| `TELEGRAM_BOT_TOKEN` | — | required if Telegram enabled | Secret |
| `TELEGRAM_CHAT_ID` | — | required if Telegram enabled | Secret |
| `ENABLE_TELEGRAM` | auto | — | `true` forces on (needs credentials), `false` forces off |
| `NOTIFY_MAX_ATTEMPTS` | `3` | ≥ 1 | Delivery attempts per notification |

At least one store must be enabled, otherwise validation fails.

## Environment file

`internal/envfile` reads `.env` from the working directory before configuration
is parsed. The format is deliberately small:

```bash
# comments and blank lines are ignored
APP_MODE=development
STORE_COUNTRIES=PL,US
export STORE_LOCALE=pl-PL        # optional export prefix
TELEGRAM_BOT_TOKEN="123:abc"     # quotes are stripped
```

Rules:

- Variables already present in the process environment are **never**
  overwritten; the real environment has precedence (useful for CI and Fly).
- A missing default `.env` is not an error. A missing file named through
  `ENV_FILE_PATH` **is** an error (exit 2).
- Inline comments are not stripped, because values may legitimately contain `#`.
- Parse errors report the file and line number.
- `.env` is git-ignored and excluded from the Docker build context.

## Defaults by mode

| Setting | `development` | `production` |
|---|---|---|
| Log format | human-readable text | JSON |
| Database | `./gamedrop.db` | `/data/offers.db` |
| Telegram | off unless credentials are set | same |

`DATABASE_PATH` only defaults when the variable is unset; an explicit value wins
in both modes.

## Region handling

Fly.io region and storefront region are unrelated settings:

```bash
# Machine runs in fra, but shopping for Poland
STORE_COUNTRIES=PL
STORE_LOCALE=pl-PL
```

Multiple countries are supported without a schema change; each country becomes a
separate fetch and a separate offer identity:

```bash
STORE_COUNTRIES=PL,US
STORE_LOCALE=pl-PL   # used for both; Epic/GOG fall back to their own defaults if odd
```

There is no US fallback: only the listed countries are queried, and every offer
records the country it was fetched for. A US-only promotion is stored and
notified as region `US`, never as available in Poland.

## Example configurations

### Local development

```bash
APP_MODE=development
LOG_LEVEL=debug
DATABASE_PATH=./gamedrop.db
STORE_COUNTRIES=PL
STORE_LOCALE=pl-PL
ENABLE_STEAM=true
ENABLE_EPIC=true
ENABLE_GOG=true
ENABLE_PRIME=false
```

### Fly production (non-secret values in `fly.toml`)

```bash
APP_MODE=production
LOG_LEVEL=info
DATABASE_PATH=/data/offers.db
STORE_COUNTRIES=PL
STORE_LOCALE=pl-PL
SOURCE_TIMEOUT_SEC=45s
MAX_CONCURRENCY=4
```

Secrets set once with `fly secrets set`:

```bash
fly secrets set --app gamedrop TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=...
```

### Debug run without notifications

```bash
TELEGRAM_BOT_TOKEN= TELEGRAM_CHAT_ID= ENABLE_TELEGRAM=false \
ENABLE_STEAM=false ENABLE_GOG=false ENABLE_PRIME=false \
LOG_LEVEL=debug go run ./cmd/gamedrop
```

## Secret handling

- Secrets belong in the environment only: Fly secrets, CI secrets, or a local
  untracked export. Never in `fly.toml`, the Dockerfile, the image, tests, or the
  repository.
- Logs never contain tokens, cookies, or request headers. The HTTP client strips
  query strings from error messages.
- `.gitignore` excludes `.env*`, `*.db`, and build output; `.dockerignore`
  excludes them from the image context as well.

## Validation matrix

| Situation | Result |
|---|---|
| `ENABLE_TELEGRAM=true`, no credentials | error naming both variables |
| Credentials present, `ENABLE_TELEGRAM=false` | notifications disabled, no error |
| No store enabled | error |
| `STORE_COUNTRIES` empty or malformed | error |
| `MAX_CONCURRENCY=0` | error |
| Invalid `LOG_LEVEL` | error |
| Database path is a directory | startup error, exit 1 |
