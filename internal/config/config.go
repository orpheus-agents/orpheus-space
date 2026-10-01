// Package config loads process environment and the base execution settings.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/pelletier/go-toml/v2"
)

type Execution struct {
	Agent struct {
		Profile          string `toml:"profile"`
		Instructions     string `toml:"instructions"`
		InstructionsFile string `toml:"instructions_file"`
	} `toml:"agent"`
	Sandbox struct {
		Template string   `toml:"template"`
		EnvFrom  []string `toml:"env_from"`
	} `toml:"sandbox"`
	Limits struct {
		RunTimeoutSeconds int    `toml:"run_timeout_seconds"`
		MaxSessionTokens  *int64 `toml:"max_session_tokens"`
	} `toml:"limits"`
}
type Config struct {
	DatabaseURL     string
	MigrationsDir   string
	APIAddress      string
	SystemAddress   string
	BrowserAuth     string
	PublicURL       string
	PublicAPIKeys   []string
	AllowedEnv      []string
	MaxRequestBytes int64
	Execution       Execution
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
func Load() (Config, error) {
	c := Config{MigrationsDir: env("ORPHEUS_MIGRATIONS_DIR", "migrations"), DatabaseURL: os.Getenv("DATABASE_URL"), BrowserAuth: env("ORPHEUS_BROWSER_AUTH", "api_only"), PublicURL: os.Getenv("ORPHEUS_PUBLIC_URL"), MaxRequestBytes: 1048576}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if _, err := DatabasePoolConfig(c.DatabaseURL); err != nil {
		return c, errors.New("invalid DATABASE_URL")
	}
	for _, entry := range []struct {
		host, port string
		dest       *string
	}{{"ORPHEUS_HOST", "ORPHEUS_PORT", &c.APIAddress}, {"ORPHEUS_SYSTEM_HOST", "ORPHEUS_SYSTEM_PORT", &c.SystemAddress}} {
		fallback := "8000"
		if entry.port == "ORPHEUS_SYSTEM_PORT" {
			fallback = "9100"
		}
		port := env(entry.port, fallback)
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("invalid %s", entry.port)
		}
		*entry.dest = net.JoinHostPort(env(entry.host, "0.0.0.0"), port)
	}
	for name, target := range map[string]*[]string{"PUBLIC_API_KEYS": &c.PublicAPIKeys, "HARNESS_ENV_ALLOWLIST": &c.AllowedEnv} {
		if err := json.Unmarshal([]byte(env(name, "[]")), target); err != nil || *target == nil {
			return c, fmt.Errorf("%s must be a JSON array", name)
		}
	}
	for _, key := range c.PublicAPIKeys {
		if strings.TrimSpace(key) == "" {
			return c, errors.New("PUBLIC_API_KEYS contains an empty key")
		}
	}
	if c.BrowserAuth != "api_only" && c.BrowserAuth != "anonymous" {
		return c, errors.New("ORPHEUS_BROWSER_AUTH must be api_only or anonymous; SAML is not available in this release")
	}
	if c.BrowserAuth == "api_only" && len(c.PublicAPIKeys) == 0 {
		return c, errors.New("PUBLIC_API_KEYS is required in api_only mode")
	}
	if c.PublicURL != "" || c.BrowserAuth == "anonymous" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" {
			return c, errors.New("ORPHEUS_PUBLIC_URL must be an HTTP(S) origin without a path")
		}
	}
	n, err := strconv.ParseInt(env("MAX_REQUEST_BYTES", "1048576"), 10, 64)
	if err != nil || n < 4096 || n > 1048576 {
		return c, errors.New("MAX_REQUEST_BYTES must be 4096..1048576")
	}
	c.MaxRequestBytes = n
	allowed, err := schedule.EnvNames(c.AllowedEnv, nil)
	if err != nil {
		return c, errors.New("invalid HARNESS_ENV_ALLOWLIST")
	}
	c.AllowedEnv = allowed
	file, err := os.Open(env("ORPHEUS_CONFIG_FILE", "orpheus-space.toml"))
	if err != nil {
		return c, errors.New("cannot open ORPHEUS_CONFIG_FILE")
	}
	defer func() { _ = file.Close() }()
	var cfg struct {
		Execution Execution `toml:"execution"`
	}
	cfg.Execution.Limits.RunTimeoutSeconds = 3600
	if err := toml.NewDecoder(file).DisallowUnknownFields().Decode(&cfg); err != nil {
		if decoded, ok := errors.AsType[*toml.DecodeError](err); ok {
			row, column := decoded.Position()
			return c, fmt.Errorf("invalid execution TOML at line %d, column %d", row, column)
		}
		return c, errors.New("invalid execution TOML")
	}
	c.Execution = cfg.Execution
	if strings.TrimSpace(c.Execution.Agent.Profile) == "" || strings.TrimSpace(c.Execution.Sandbox.Template) == "" {
		return c, errors.New("execution profile and sandbox template are required")
	}
	if c.Execution.Limits.RunTimeoutSeconds < 1 || c.Execution.Limits.MaxSessionTokens != nil && *c.Execution.Limits.MaxSessionTokens < 1 {
		return c, errors.New("invalid execution limits")
	}
	if c.Execution.Agent.InstructionsFile != "" {
		if c.Execution.Agent.Instructions != "" {
			return c, errors.New("choose instructions or instructions_file")
		}
		raw, err := os.ReadFile(c.Execution.Agent.InstructionsFile)
		if err != nil {
			return c, errors.New("cannot read execution instructions_file")
		}
		c.Execution.Agent.Instructions = string(raw)
	}
	base, err := schedule.EnvNames(c.Execution.Sandbox.EnvFrom, c.AllowedEnv)
	if err != nil {
		return c, errors.New("base env_from must contain distinct allowlisted names")
	}
	c.Execution.Sandbox.EnvFrom = base
	slices.Sort(c.PublicAPIKeys)
	return c, nil
}
