package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectionConfigDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space.toml")
	_, _, err := selectionDefaults(path)
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"syntax", "[execution.agent]\nprofile = secret-literal\n", "line 2, column"},
		{"type", "[execution.agent]\nprofile = ['secret-literal']\n", "line 2, column"},
		{"missing defaults", "[execution.agent]\nprofile = 'secret-literal'\n", "requires execution.agent.profile and execution.sandbox.template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err := selectionDefaults(path)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-literal") {
				t.Fatal(err)
			}
		})
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(path, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0600) })
		_, _, err := selectionDefaults(path)
		if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), path) {
			t.Fatal(err)
		}
	}
}
