// Package setup implements the interactive first-run wizard behind
// `gamedrop --setup`. It exists so that a non-technical user never has to edit a
// file by hand: the wizard asks a few plain questions, writes or updates .env,
// verifies the Telegram bot, and can run the first collection cycle.
package setup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Guliveer/GameDrop/internal/envfile"
	"github.com/Guliveer/GameDrop/internal/notify"
)

// Options configure the wizard. In, Out and RunCycle are required.
type Options struct {
	// WorkDir is the directory .env is written to (normally the repository root).
	WorkDir string
	In      io.Reader
	Out     io.Writer
	// TelegramBaseURL overrides the Bot API root (tests).
	TelegramBaseURL string
	// HTTPClient overrides the client used for Telegram calls (tests).
	HTTPClient *http.Client
	// RunCycle executes one collection cycle and returns a process exit code.
	RunCycle func(ctx context.Context) int
}

// Run asks the setup questions and returns the process exit code.
func Run(ctx context.Context, opts Options) int {
	out := opts.Out
	if out == nil || opts.In == nil {
		return 2
	}
	if opts.WorkDir == "" {
		opts.WorkDir = "."
	}
	baseURL := opts.TelegramBaseURL
	if baseURL == "" {
		baseURL = notify.DefaultTelegramBaseURL
	}

	p := &prompter{in: bufio.NewReader(opts.In), out: out}

	p.say("")
	p.say("GameDrop setup")
	p.say("==============")
	p.say("A few questions and you are done. Press Enter to accept the value in [brackets].")
	p.say("")

	path := envPath(opts.WorkDir)
	existing := readValues(path)

	country := strings.ToUpper(strings.TrimSpace(
		p.ask("Which country's store offers should we watch? Two-letter code, e.g. PL, US, GB", firstNonEmpty(existing["STORE_COUNTRIES"], "PL")),
	))
	if !validCountry(country) {
		p.say("  %q is not a two-letter country code; using PL.", country)
		country = "PL"
	}

	values := map[string]string{"STORE_COUNTRIES": country}
	if strings.TrimSpace(existing["STORE_LOCALE"]) == "" {
		values["STORE_LOCALE"] = localeFor(country)
	}

	token := strings.TrimSpace(p.ask("Telegram bot token from @BotFather (Enter skips notifications)", existing["TELEGRAM_BOT_TOKEN"]))
	chatID := strings.TrimSpace(existing["TELEGRAM_CHAT_ID"])
	if token != "" {
		api := telegramAPI{
			client:  opts.HTTPClient,
			baseURL: baseURL,
			token:   token,
		}
		if api.client == nil {
			api.client = &http.Client{Timeout: 20 * time.Second}
		}

		if username, err := api.botUsername(ctx); err != nil {
			p.say("  Telegram did not accept the token: %v", err)
			p.say("  Notifications stay off for now; rerun the setup when you have the right token.")
			token = ""
			chatID = ""
		} else {
			p.say("  Connected to the bot @%s.", username)
			if chatID == "" {
				chatID = findChat(ctx, p, api)
			}
			if chatID == "" {
				chatID = strings.TrimSpace(p.ask("Paste the chat ID yourself (Enter skips notifications)", ""))
			}
			if chatID != "" {
				values["TELEGRAM_CHAT_ID"] = chatID
				if p.confirm("Send a test message now?", true) {
					if err := api.sendTest(ctx, chatID); err != nil {
						p.say("  The test message failed: %v", err)
						p.say("  The settings are still saved, so you can fix it later.")
					} else {
						p.say("  Test message sent. Check Telegram.")
					}
				}
			}
		}
	}
	if token != "" {
		values["TELEGRAM_BOT_TOKEN"] = token
	}

	if err := writeEnvFile(path, opts.WorkDir, values); err != nil {
		p.say("")
		p.say("Could not write %s: %v", path, err)
		return 2
	}

	p.say("")
	p.say("Saved %s", path)
	p.say("  country:       %s", country)
	p.say("  notifications: %s", describeNotifications(values["TELEGRAM_BOT_TOKEN"], values["TELEGRAM_CHAT_ID"]))

	if !p.interactive {
		p.say("")
		p.say("Next: run scripts\\run.bat (Windows) or ./scripts/run.sh to collect offers.")
		return 0
	}

	if !p.confirm("Run the first collection now?", true) {
		p.say("")
		p.say("Fine. Later, start a collection with scripts\\run.bat (Windows) or ./scripts/run.sh.")
		return 0
	}

	// Apply the file we just wrote before the cycle reads its configuration, so
	// the freshly entered values are used even though the process started earlier.
	if _, err := envfile.Load(path); err != nil {
		p.say("Could not load %s: %v", path, err)
		return 2
	}

	p.say("")
	p.say("Collecting offers. This usually takes a few seconds.")
	code := opts.RunCycle(ctx)
	p.say("")
	switch code {
	case 0:
		p.say("Done. Offers are stored in the database file next to this folder.")
	case 1:
		p.say("The cycle did not finish. Details are in the log lines above; running it again is safe.")
	default:
		p.say("Some settings are still wrong. The message above lists them; rerun `gamedrop --setup` afterwards.")
	}
	p.say("Next runs: scripts\\run.bat (Windows) or ./scripts/run.sh.")
	return code
}

// findChat looks for the chat of the most recent message to the bot, asking the
// user to press Start when nothing has arrived yet.
func findChat(ctx context.Context, p *prompter, api telegramAPI) string {
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			p.say("  Open Telegram, find your bot and press Start, then press Enter here.")
			p.pause()
		}
		id, name, err := api.findChatID(ctx)
		if err != nil {
			p.say("  Could not read Telegram updates: %v", err)
			return ""
		}
		if id == "" {
			p.say("  No messages to the bot yet.")
			continue
		}
		label := name
		if label == "" {
			label = id
		}
		p.say("  Found a chat: %s", label)
		return id
	}
	return ""
}

// describeNotifications explains the resulting notification state in one line.
func describeNotifications(token, chatID string) string {
	switch {
	case token == "":
		return "off (no bot token)"
	case chatID == "":
		return "off (no chat ID)"
	default:
		return "on (Telegram)"
	}
}

// localeFor maps a country to the store locale used for titles and currency.
func localeFor(country string) string {
	if locale, ok := countryLocales[country]; ok {
		return locale
	}
	return "en-US"
}

var countryLocales = map[string]string{
	"AU": "en-AU",
	"BR": "pt-BR",
	"CA": "en-CA",
	"CN": "zh-CN",
	"DE": "de-DE",
	"ES": "es-ES",
	"FR": "fr-FR",
	"GB": "en-GB",
	"IN": "en-IN",
	"IT": "it-IT",
	"JP": "ja-JP",
	"KR": "ko-KR",
	"NL": "nl-NL",
	"PL": "pl-PL",
	"RU": "ru-RU",
	"TR": "tr-TR",
	"UA": "uk-UA",
	"US": "en-US",
}

// validCountry accepts any two-letter code: the stores, not the wizard, decide
// which regions they actually serve.
func validCountry(code string) bool {
	if len(code) != 2 {
		return false
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// prompter reads answers from a terminal. Reading fails at end of input, which is
// how piped or non-interactive runs fall back to defaults.
type prompter struct {
	in          *bufio.Reader
	out         io.Writer
	interactive bool
}

func (p *prompter) say(format string, args ...any) {
	fmt.Fprintf(p.out, format+"\n", args...)
}

// ask prints the question with its default and returns the trimmed answer.
func (p *prompter) ask(question, fallback string) string {
	if fallback != "" {
		p.say("%s [%s]:", question, fallback)
	} else {
		p.say("%s:", question)
	}
	fmt.Fprint(p.out, "> ")

	line, err := p.in.ReadString('\n')
	if err != nil {
		return fallback
	}
	p.interactive = true
	answer := strings.TrimSpace(line)
	if answer == "" {
		return fallback
	}
	return answer
}

// confirm asks a yes/no question; an empty answer uses defaultValue.
func (p *prompter) confirm(question string, defaultValue bool) bool {
	hint := "Y/n"
	if !defaultValue {
		hint = "y/N"
	}
	answer := strings.ToLower(p.ask(question+" ("+hint+")", ""))
	switch answer {
	case "":
		return defaultValue
	case "y", "yes", "t", "tak":
		return true
	case "n", "no", "nie":
		return false
	default:
		return defaultValue
	}
}

// pause waits for Enter without using the answer.
func (p *prompter) pause() {
	fmt.Fprint(p.out, "> ")
	_, _ = p.in.ReadString('\n')
}
