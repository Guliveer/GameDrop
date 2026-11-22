// Package envfile loads simple KEY=VALUE files (`.env`) into the process
// environment. It exists so local development works with a single documented
// file, without pulling in a third-party dotenv dependency and without changing
// production behaviour: real environment variables always win, and a missing
// file is not an error unless it was requested explicitly.
package envfile

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultPath is the file loaded when ENV_FILE_PATH is not set.
const DefaultPath = ".env"

// PathVariable names the environment variable that overrides the file path.
const PathVariable = "ENV_FILE_PATH"

// Result describes what LoadDefault did.
type Result struct {
	// Path is the file that was consulted.
	Path string
	// Found reports whether the file existed.
	Found bool
	// Loaded counts variables copied into the environment.
	Loaded int
	// Skipped counts variables that were already set and therefore kept.
	Skipped int
}

// LoadDefault loads ENV_FILE_PATH or ".env".
//
// A missing default file is ignored (GameDrop runs happily on real environment
// variables only). A missing file that was named explicitly through
// ENV_FILE_PATH is an error, because the operator clearly expected it.
func LoadDefault() (Result, error) {
	path := strings.TrimSpace(os.Getenv(PathVariable))
	explicit := path != ""
	if !explicit {
		path = DefaultPath
	}

	result, err := Load(path)
	if err != nil {
		return result, err
	}
	if !result.Found && explicit {
		return result, fmt.Errorf("env file %q (from %s) does not exist", path, PathVariable)
	}
	return result, nil
}

// Load reads path and sets every key that is not already present in the
// environment. Lines support:
//
//   - blank lines and lines starting with '#'
//   - an optional `export ` prefix
//   - values wrapped in single or double quotes (double quotes understand the
//     usual Go escapes, e.g. "\n")
//   - surrounding whitespace
//
// Inline comments are not stripped: a value may legitimately contain '#'.
func Load(path string) (Result, error) {
	result := Result{Path: path}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("env file: open %s: %w", path, err)
	}
	defer file.Close()
	result.Found = true

	scanner := bufio.NewScanner(file)
	// Allow long tokens (tokens, JSON blobs) without hitting the default limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		key, value, ok, err := parseLine(scanner.Text())
		if err != nil {
			return result, fmt.Errorf("env file %s:%d: %w", path, lineNumber, err)
		}
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			result.Skipped++
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return result, fmt.Errorf("env file %s:%d: set %s: %w", path, lineNumber, key, err)
		}
		result.Loaded++
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("env file: read %s: %w", path, err)
	}
	return result, nil
}

// parseLine turns one line into a key/value pair. ok is false for blank lines
// and comments.
func parseLine(line string) (key, value string, ok bool, err error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false, nil
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")

	key, rawValue, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false, fmt.Errorf("expected KEY=VALUE")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false, fmt.Errorf("empty variable name")
	}
	if strings.ContainsAny(key, " \t") {
		return "", "", false, fmt.Errorf("invalid variable name %q", key)
	}

	value, err = unquote(strings.TrimSpace(rawValue))
	if err != nil {
		return "", "", false, fmt.Errorf("%s: %w", key, err)
	}
	return key, value, true, nil
}

// unquote strips one layer of matching single or double quotes. A value that
// opens a quote without closing it is a parse error rather than a value with a
// stray quote character.
func unquote(value string) (string, error) {
	if value == "" {
		return value, nil
	}
	switch value[0] {
	case '\'':
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return value[1 : len(value)-1], nil
	case '"':
		if len(value) < 2 || value[len(value)-1] != '"' {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value: %w", err)
		}
		return unquoted, nil
	default:
		return value, nil
	}
}
