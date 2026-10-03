package config

import (
	"os"
	"slices"
	"testing"
)

func TestAccessConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		want         []string
		invalid      bool
	}{
		{name: "omitted"},
		{name: "empty", config: "[access]\nadmin_emails=[]\n"},
		{name: "normalized", config: "[access]\nadmin_emails=[' ADMIN@Example.com ', 'other@example.com']\n", want: []string{"admin@example.com", "other@example.com"}},
		{name: "duplicates", config: "[access]\nadmin_emails=['admin@example.com', ' ADMIN@example.com ']\n", invalid: true},
		{name: "invalid", config: "[access]\nadmin_emails=['invalid']\n", invalid: true},
		{name: "blank", config: "[access]\nadmin_emails=['']\n", invalid: true},
		{name: "unknown", config: "[access]\nadmins=[]\n", invalid: true},
		{name: "wrong type", config: "[access]\nadmin_emails='admin@example.com'\n", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := fixture(t)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append([]byte(tc.config), raw...), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if (err != nil) != tc.invalid {
				t.Fatal(err)
			}
			if !tc.invalid && !slices.Equal(cfg.Access.AdminEmails, tc.want) {
				t.Fatal(cfg.Access)
			}
			t.Setenv("ORPHEUS_BASE_URL", "http://core.test")
			t.Setenv("ORPHEUS_API_KEY", "key")
			if _, err := LoadWorker(); (err != nil) != tc.invalid {
				t.Fatal("worker", err)
			}
		})
	}
}
