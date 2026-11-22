# Getting started

This page is for a first run, with no prior knowledge of Go, terminals, or the
stores' APIs. It takes about five minutes and needs no hand-edited files: the
setup wizard writes the configuration for you.

If you already know your way around a terminal, the short version is
`./scripts/setup.sh` (or `scripts\setup.bat` on Windows), then
`./scripts/run.sh`.

## 1. Install Go

Go is the only prerequisite (version 1.27 or newer).

- **Windows:** download the `.msi` installer from <https://go.dev/dl/>, run it and
  accept the defaults. Close and reopen the Command Prompt afterwards.
- **macOS:** download the `.pkg` installer from the same page, or run
  `brew install go`.
- **Linux:** use your package manager, or unpack the official tarball into
  `/usr/local` and add `/usr/local/go/bin` to `PATH`.

Check it worked by opening a new terminal and running:

```bash
go version
```

You should see something like `go version go1.27.0 windows/amd64`.

## 2. Start the wizard

- **Windows:** open the GameDrop folder in File Explorer and double-click
  `scripts\setup.bat`. (If Windows asks, choose "Run"; the file is a plain text
  script you can read.)
- **Linux/macOS:** in a terminal, from the project folder:

  ```bash
  ./scripts/setup.sh
  ```

The wizard prints a few questions. Press Enter to accept the value in
`[brackets]`.

### Question 1 — country

Which store region should be watched, as a two-letter code: `PL`, `US`, `GB`,
`DE`, and so on. The default is `PL`.

This is the *store* region, not where the computer is. Multiple countries can be
added later by editing `STORE_COUNTRIES=PL,US` in `.env`.

### Question 2 — Telegram bot token (optional)

Skip this with Enter if you only want a local database for now; notifications can
be added later by rerunning the wizard.

To get a token:

1. In Telegram, search for **@BotFather** and open it.
2. Send `/newbot`.
3. Give it a name (for example `My GameDrop`) and a username ending in `bot`
   (for example `my_gamedrop_bot`).
4. BotFather replies with a token like `123456789:AAH...`. Copy it and paste it
   into the wizard.

### Question 3 — chat

After the token is accepted, the wizard looks for the chat that wrote to your bot.

1. Open your new bot in Telegram and press **Start** (send `/start`).
2. Press Enter in the wizard when it says it is waiting.
3. The wizard finds your chat ID automatically, and can send a test message —
   answer `y`.

If the automatic lookup fails, the wizard asks you to paste the chat ID; you can
also leave it empty and rerun the wizard later.

## 3. First cycle

The last question offers to run the first collection. Say yes and the wizard
collects offers from Steam, Epic and GOG for your country, stores them in a local
SQLite file, and sends any eligible Telegram messages.

You will see log lines like:

```text
level=INFO msg="source scanned" source=epic country=PL status=success offers_found=2 offers_new=2
level=INFO msg="collection cycle finished" status=succeeded duration_ms=471
```

Zero offers is normal on days without a giveaway — nothing is wrong.

## 4. Everyday use

| What | Windows | Linux/macOS |
|---|---|---|
| Run one cycle | `scripts\run.bat` | `./scripts/run.sh` |
| Rerun the wizard | `scripts\setup.bat` | `./scripts/setup.sh` |
| Where settings live | `.env` (next to the program) | `.env` |
| Where offers are stored | `gamedrop.db` | `gamedrop.db` |

`gamedrop.db` can be opened with [DB Browser for SQLite](https://sqlitebrowser.org/)
to browse the collected offers. It is git-ignored and safe to keep.

Rerunning the wizard is safe: it keeps your existing answers as defaults and
preserves comments and unrelated settings in `.env`.

## 5. What happens next

Nothing runs by itself on your computer. Each cycle is one run of the program.
To get automatic checks without keeping a computer on, deploy the worker — the
guide is [Fly.io deployment](fly-deployment.md), and the schedule there is a
Scheduled Machine.

## Troubleshooting

| Symptom | Meaning and fix |
|---|---|
| `GameDrop needs Go ...` | Go is not installed (or the terminal was opened before installing). Install it, open a new terminal, run the wizard again. |
| `Telegram rejected the bot token` | The token is incomplete or from another bot. Copy the whole token from @BotFather and rerun the wizard. |
| `Telegram does not know that chat yet` | The bot has never been messaged. Open it in Telegram, press Start, then retry. |
| `configuration error:` followed by a list | One or more `.env` values are wrong; the message names them. Fix those lines or rerun the wizard. |
| No Telegram messages | Notifications are off (no token or chat ID), or no offer qualified (offers that start later notify when they start). |
| `offers_found=0` for Steam or GOG | No 100% discount is live right now. Epic usually has one weekly. |
| `database is locked` | Two cycles ran at the same time. Run one at a time. |

Exit codes: `0` the cycle completed (possibly with some stores failing), `1` the
cycle failed, `2` configuration is invalid.

## Manual configuration

Everything the wizard writes is a normal environment variable, documented in
[configuration.md](configuration.md) and [`../.env.example`](../.env.example).
Values already present in the environment win over `.env`, so a one-off override
works without editing anything:

```bash
LOG_LEVEL=debug ./scripts/run.sh
```
