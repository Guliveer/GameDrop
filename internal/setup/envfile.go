package setup

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Guliveer/GameDrop/internal/envfile"
)

// envPath resolves the file the wizard writes: ENV_FILE_PATH when it is set
// (matching the loader), otherwise .env in the working directory.
func envPath(workDir string) string {
	if explicit := strings.TrimSpace(os.Getenv(envfile.PathVariable)); explicit != "" {
		if filepath.IsAbs(explicit) {
			return explicit
		}
		return filepath.Join(workDir, explicit)
	}
	return filepath.Join(workDir, envfile.DefaultPath)
}

// readValues returns the KEY=VALUE pairs already present in path. Unreadable or
// malformed files simply yield what could be parsed; the wizard rewrites them.
func readValues(path string) map[string]string {
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok, err := envfile.ParseLine(line)
		if err != nil || !ok {
			continue
		}
		values[key] = value
	}
	return values
}

// writeEnvFile writes values into path, keeping every other line of the file it
// found there. New files start from the commented .env.example template so the
// result stays self-documenting.
func writeEnvFile(path, workDir string, values map[string]string) error {
	template := ""
	if data, err := os.ReadFile(path); err == nil {
		template = string(data)
	} else if data, err := os.ReadFile(filepath.Join(workDir, ".env.example")); err == nil {
		template = string(data)
	}

	rendered := renderEnv(template, values)
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(rendered), 0o600)
}

// renderEnv rewrites the keys present in values and appends the rest. Comments,
// ordering and unrelated keys are preserved so hand-written notes survive.
func renderEnv(template string, values map[string]string) string {
	remaining := make(map[string]string, len(values))
	for key, value := range values {
		remaining[key] = value
	}

	var lines []string
	if strings.TrimSpace(template) != "" {
		lines = strings.Split(strings.TrimRight(template, "\n"), "\n")
	}

	out := make([]string, 0, len(lines)+len(values)+2)
	written := map[string]bool{}
	for _, line := range lines {
		key, _, ok, err := envfile.ParseLine(line)
		if err == nil && ok {
			if value, wanted := values[key]; wanted {
				out = append(out, key+"="+formatValue(value))
				written[key] = true
				delete(remaining, key)
				continue
			}
		}
		out = append(out, line)
	}

	if len(remaining) > 0 {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "# Chosen by `gamedrop --setup`.")
		keys := make([]string, 0, len(remaining))
		for key := range remaining {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if written[key] {
				continue
			}
			out = append(out, key+"="+formatValue(remaining[key]))
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// formatValue quotes a value the loader would otherwise misread. Empty values
// stay empty so that optional settings remain easy to spot.
func formatValue(value string) string {
	if value == "" {
		return ""
	}
	if !strings.ContainsAny(value, " \t#'\"\\") {
		return value
	}
	escaped := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n").Replace(value)
	return "\"" + escaped + "\""
}
