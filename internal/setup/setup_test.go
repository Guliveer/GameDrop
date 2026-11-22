package setup

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exampleTemplate = `# GameDrop — environment variables
# Comments survive a rewrite.
STORE_COUNTRIES=PL
STORE_LOCALE=pl-PL
TELEGRAM_BOT_TOKEN=
TELEGRAM_CHAT_ID=
`

func TestRenderEnvReplacesValuesAndKeepsComments(t *testing.T) {
	rendered := renderEnv(exampleTemplate, map[string]string{
		"STORE_COUNTRIES":    "DE",
		"TELEGRAM_BOT_TOKEN": "123:abc",
	})

	if !strings.Contains(rendered, "# Comments survive a rewrite.") {
		t.Errorf("comment was dropped:\n%s", rendered)
	}
	if !strings.Contains(rendered, "STORE_COUNTRIES=DE") {
		t.Errorf("country was not replaced:\n%s", rendered)
	}
	if !strings.Contains(rendered, "TELEGRAM_BOT_TOKEN=123:abc") {
		t.Errorf("token was not written:\n%s", rendered)
	}
	if strings.Contains(rendered, "STORE_COUNTRIES=PL") {
		t.Errorf("old country survived:\n%s", rendered)
	}
}

func TestRenderEnvAppendsMissingKeys(t *testing.T) {
	rendered := renderEnv("# header\nSTORE_COUNTRIES=PL\n", map[string]string{
		"TELEGRAM_CHAT_ID": "42",
	})

	if !strings.Contains(rendered, "STORE_COUNTRIES=PL") {
		t.Errorf("existing value lost:\n%s", rendered)
	}
	if !strings.Contains(rendered, "TELEGRAM_CHAT_ID=42") {
		t.Errorf("new key was not appended:\n%s", rendered)
	}
	if strings.Count(rendered, "TELEGRAM_CHAT_ID=") != 1 {
		t.Errorf("key appears more than once:\n%s", rendered)
	}
}

func TestFormatValueQuotesValuesTheLoaderWouldMisread(t *testing.T) {
	tests := map[string]string{
		"":             "",
		"PL":           "PL",
		"123:abc-def":  "123:abc-def",
		"two words":    `"two words"`,
		"has#hash":     `"has#hash"`,
		`quote"inside`: `"quote\"inside"`,
	}
	for input, want := range tests {
		if got := formatValue(input); got != want {
			t.Errorf("formatValue(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWriteEnvFileStartsFromExampleTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(exampleTemplate), 0o644); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, ".env")
	if err := writeEnvFile(path, dir, map[string]string{"STORE_COUNTRIES": "US"}); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "# GameDrop — environment variables") {
		t.Errorf("template header missing:\n%s", content)
	}
	if !strings.Contains(content, "STORE_COUNTRIES=US") {
		t.Errorf("value not written:\n%s", content)
	}
}

func TestRunNonInteractiveUsesDefaultsAndSkipsCycle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(exampleTemplate), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ran := false
	code := Run(context.Background(), Options{
		WorkDir: dir,
		In:      strings.NewReader(""),
		Out:     &out,
		RunCycle: func(context.Context) int {
			ran = true
			return 0
		},
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	if ran {
		t.Error("cycle ran without confirmation")
	}

	values := readValues(filepath.Join(dir, ".env"))
	if values["STORE_COUNTRIES"] != "PL" {
		t.Errorf("STORE_COUNTRIES = %q, want PL", values["STORE_COUNTRIES"])
	}
	if values["STORE_LOCALE"] != "pl-PL" {
		t.Errorf("STORE_LOCALE = %q, want pl-PL", values["STORE_LOCALE"])
	}
	if values["TELEGRAM_BOT_TOKEN"] != "" {
		t.Errorf("token should stay empty, got %q", values["TELEGRAM_BOT_TOKEN"])
	}
}

func TestRunInvalidCountryFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer

	code := Run(context.Background(), Options{
		WorkDir:  dir,
		In:       strings.NewReader("xyz\n\n"),
		Out:      &out,
		RunCycle: func(context.Context) int { return 0 },
	})
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out.String())
	}

	values := readValues(filepath.Join(dir, ".env"))
	if values["STORE_COUNTRIES"] != "PL" {
		t.Errorf("STORE_COUNTRIES = %q, want PL", values["STORE_COUNTRIES"])
	}
	if !strings.Contains(out.String(), "not a two-letter country code") {
		t.Errorf("missing warning:\n%s", out.String())
	}
}

func TestRunRejectsTokenTelegramDislikes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	var out bytes.Buffer
	code := Run(context.Background(), Options{
		WorkDir:         dir,
		In:              strings.NewReader("DE\n999:bad\n"), // country, token
		Out:             &out,
		TelegramBaseURL: server.URL,
		RunCycle:        func(context.Context) int { return 0 },
	})
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out.String())
	}

	values := readValues(filepath.Join(dir, ".env"))
	if values["TELEGRAM_BOT_TOKEN"] != "" {
		t.Errorf("rejected token was saved: %q", values["TELEGRAM_BOT_TOKEN"])
	}
	if !strings.Contains(out.String(), "did not accept the token") {
		t.Errorf("missing plain-language error:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "notifications: off") {
		t.Errorf("summary should say notifications are off:\n%s", out.String())
	}
}

func TestRunDetectsChatAndSendsTestMessage(t *testing.T) {
	var sent []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"username":"gamedrop_bot","first_name":"GameDrop"}}`))
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"message":{"chat":{"id":4242,"first_name":"Ola"}}}]}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sent = append(sent, r.URL.Path)
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	var out bytes.Buffer
	code := Run(context.Background(), Options{
		WorkDir:         dir,
		In:              strings.NewReader("DE\n123:abc\n\nn\n"), // country, token, test message, no cycle
		Out:             &out,
		TelegramBaseURL: server.URL,
		RunCycle:        func(context.Context) int { return 0 },
	})
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out.String())
	}

	values := readValues(filepath.Join(dir, ".env"))
	if values["TELEGRAM_BOT_TOKEN"] != "123:abc" {
		t.Errorf("token = %q", values["TELEGRAM_BOT_TOKEN"])
	}
	if values["TELEGRAM_CHAT_ID"] != "4242" {
		t.Errorf("chat ID = %q, want 4242", values["TELEGRAM_CHAT_ID"])
	}
	if len(sent) != 1 {
		t.Errorf("sendMessage calls = %d, want 1\n%s", len(sent), out.String())
	}
	if !strings.Contains(out.String(), "notifications: on") {
		t.Errorf("summary should report notifications on:\n%s", out.String())
	}
}

func TestRunRunsCycleWhenConfirmed(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	ran := false

	code := Run(context.Background(), Options{
		WorkDir: dir,
		In:      strings.NewReader("PL\n\n\n"), // country, skip telegram, confirm the cycle
		Out:     &out,
		RunCycle: func(context.Context) int {
			ran = true
			return 0
		},
	})
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out.String())
	}
	if !ran {
		t.Errorf("cycle did not run\n%s", out.String())
	}
}

func TestDescribeTelegramError(t *testing.T) {
	tests := []struct {
		status  int
		payload string
		want    string
	}{
		{http.StatusUnauthorized, `{"description":"Unauthorized"}`, "rejected the bot token"},
		{http.StatusBadRequest, `{"description":"Bad Request: chat not found"}`, "press Start"},
		{http.StatusBadRequest, `{"description":"Forbidden: bot was blocked by the user"}`, "blocked this bot"},
		{http.StatusBadRequest, `{"description":"weird"}`, "weird"},
	}
	for _, tc := range tests {
		got := describeTelegramError(tc.status, []byte(tc.payload))
		if !strings.Contains(got, tc.want) {
			t.Errorf("describeTelegramError(%d, %s) = %q, want it to contain %q", tc.status, tc.payload, got, tc.want)
		}
	}
}

func TestLocaleFor(t *testing.T) {
	tests := map[string]string{
		"PL": "pl-PL",
		"DE": "de-DE",
		"GB": "en-GB",
		"ZZ": "en-US",
	}
	for country, want := range tests {
		if got := localeFor(country); got != want {
			t.Errorf("localeFor(%q) = %q, want %q", country, got, want)
		}
	}
}
