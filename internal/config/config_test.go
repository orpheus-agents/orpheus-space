package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "space.toml")
	if err := os.WriteFile(path, []byte("[execution.agent]\nprofile='default'\n[execution.sandbox]\ntemplate='codex'\nenv_from=['A']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"DATABASE_URL": "postgres://user:pass@localhost/db", "ORPHEUS_CONFIG_FILE": path, "PUBLIC_API_KEYS": "[\"test-key\"]", "HARNESS_ENV_ALLOWLIST": "[\"A\"]", "ORPHEUS_BROWSER_AUTH": "api_only", "ORPHEUS_PUBLIC_URL": "", "MAX_REQUEST_BYTES": "1048576", "ORPHEUS_PORT": "8000", "ORPHEUS_SYSTEM_PORT": "9100"} {
		t.Setenv(k, v)
	}
	return path
}
func TestConfigModesAndEnvironment(t *testing.T) {
	fixture(t)
	cfg, err := Load()
	if err != nil || cfg.BrowserAuth != "api_only" || cfg.Execution.Limits.RunTimeoutSeconds != 3600 {
		t.Fatal(cfg, err)
	}
	t.Setenv("PUBLIC_API_KEYS", "[]")
	if _, err := Load(); err == nil {
		t.Fatal("missing keys accepted")
	}
	t.Setenv("ORPHEUS_BROWSER_AUTH", "anonymous")
	t.Setenv("ORPHEUS_PUBLIC_URL", "http://localhost:8010")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, value string }{{"ORPHEUS_BROWSER_AUTH", "saml"}, {"ORPHEUS_BROWSER_AUTH", "typo"}, {"PUBLIC_API_KEYS", "null"}, {"PUBLIC_API_KEYS", "[\"\"]"}, {"HARNESS_ENV_ALLOWLIST", "[]"}, {"HARNESS_ENV_ALLOWLIST", "[\"A\",\"A\"]"}, {"ORPHEUS_PUBLIC_URL", "https://user:pass@example.com"}, {"ORPHEUS_PUBLIC_URL", "http://localhost:8010/"}, {"ORPHEUS_PORT", "0"}, {"MAX_REQUEST_BYTES", "0"}, {"DATABASE_URL", ""}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
func TestExecutionConfiguration(t *testing.T) {
	path := fixture(t)
	instructions := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructions, []byte("Inspect incidents"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{"[execution.agent]\nprofile='default'\ninstructions_file='" + instructions + "'\n[execution.sandbox]\ntemplate='codex'", true},
		{"[execution.agent]\nprofile='default'\ninstructions_file='" + instructions + "'\ninstructions='both'\n[execution.sandbox]\ntemplate='codex'", false},
		{"[execution.agent]\nprofile='default'\nunknown='field'\n[execution.sandbox]\ntemplate='codex'", false},
		{"[execution.agent]\nprofile='default'", false},
		{"[execution.agent]\nprofile='default'\n[execution.sandbox]\ntemplate='codex'\n[execution.limits]\nrun_timeout_seconds=0", false},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load()
		if (err == nil) != tc.valid {
			t.Fatalf("%v %v", cfg, err)
		}
		if tc.valid && cfg.Execution.Agent.Instructions != "Inspect incidents" {
			t.Fatal("instructions not loaded")
		}
	}
}

func TestTOMLErrorPositionWithoutValues(t *testing.T) {
	path := fixture(t)
	for _, body := range []string{"[execution.agent]\nprofile=secret-value", "[execution.agent]\nunknown='secret-value'"} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "line 2, column") || strings.Contains(err.Error(), "secret-value") {
			t.Fatal(err)
		}
	}
}
