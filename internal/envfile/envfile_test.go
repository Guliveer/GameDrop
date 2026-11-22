package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolated clears every variable the test file sets and restores the process
// environment afterwards.
func isolated(t *testing.T) {
	t.Helper()
	// t.Setenv registers the original value for restoration; the empty value is
	// irrelevant because GameDrop treats empty as unset.
	for _, key := range []string{"GAMEDROP_TEST_A", "GAMEDROP_TEST_B", "GAMEDROP_TEST_C", "GAMEDROP_TEST_D", PathVariable} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestLoadParsesCommonSyntax(t *testing.T) {
	isolated(t)
	path := writeFile(t, strings.Join([]string{
		"# comment",
		"",
		"GAMEDROP_TEST_A=simple",
		"export GAMEDROP_TEST_B=with export prefix",
		`GAMEDROP_TEST_C="quoted value"`,
		"GAMEDROP_TEST_D='single quoted'",
	}, "\n"))

	result, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !result.Found || result.Loaded != 4 {
		t.Fatalf("result = %+v", result)
	}

	want := map[string]string{
		"GAMEDROP_TEST_A": "simple",
		"GAMEDROP_TEST_B": "with export prefix",
		"GAMEDROP_TEST_C": "quoted value",
		"GAMEDROP_TEST_D": "single quoted",
	}
	for key, expected := range want {
		if got := os.Getenv(key); got != expected {
			t.Errorf("%s = %q, want %q", key, got, expected)
		}
	}
}

func TestLoadDoesNotOverrideExistingEnvironment(t *testing.T) {
	isolated(t)
	t.Setenv("GAMEDROP_TEST_A", "from-environment")
	path := writeFile(t, "GAMEDROP_TEST_A=from-file\nGAMEDROP_TEST_B=from-file\n")

	result, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := os.Getenv("GAMEDROP_TEST_A"); got != "from-environment" {
		t.Errorf("existing variable was overridden: %q", got)
	}
	if got := os.Getenv("GAMEDROP_TEST_B"); got != "from-file" {
		t.Errorf("new variable not set: %q", got)
	}
	if result.Skipped != 1 || result.Loaded != 1 {
		t.Errorf("result = %+v, want 1 loaded and 1 skipped", result)
	}
}

func TestLoadMissingDefaultFileIsNotAnError(t *testing.T) {
	isolated(t)
	result, err := Load(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("missing file must not fail: %v", err)
	}
	if result.Found || result.Loaded != 0 {
		t.Errorf("result = %+v", result)
	}
}

func TestLoadDefaultRequiresExplicitFile(t *testing.T) {
	isolated(t)
	t.Setenv(PathVariable, filepath.Join(t.TempDir(), "absent.env"))
	if _, err := LoadDefault(); err == nil {
		t.Fatal("an explicitly configured missing file must be an error")
	}
}

func TestLoadDefaultUsesPathVariable(t *testing.T) {
	isolated(t)
	path := writeFile(t, "GAMEDROP_TEST_A=via-env-file-path\n")
	t.Setenv(PathVariable, path)

	result, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if result.Path != path || result.Loaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	if got := os.Getenv("GAMEDROP_TEST_A"); got != "via-env-file-path" {
		t.Errorf("GAMEDROP_TEST_A = %q", got)
	}
}

func TestLoadRejectsMalformedLines(t *testing.T) {
	isolated(t)
	cases := map[string]string{
		"no equals sign":   "JUST_A_WORD\n",
		"empty name":       "=value\n",
		"space in name":    "BAD NAME=value\n",
		"bad double quote": "GAMEDROP_TEST_A=\"unterminated\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeFile(t, content)); err == nil {
				t.Fatal("expected a parse error")
			}
		})
	}
}

func TestLoadReportsLineNumbers(t *testing.T) {
	isolated(t)
	_, err := Load(writeFile(t, "# ok\n\nBROKEN\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), ":3:") {
		t.Errorf("error should name line 3: %v", err)
	}
}

func TestDoubleQuotedEscapes(t *testing.T) {
	isolated(t)
	path := writeFile(t, `GAMEDROP_TEST_A="line1\nline2"`+"\n")
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := os.Getenv("GAMEDROP_TEST_A"); got != "line1\nline2" {
		t.Errorf("value = %q", got)
	}
}
