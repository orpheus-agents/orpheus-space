// Package schedule defines schedule inputs independently of HTTP and storage.
package schedule

import (
	"encoding/json"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type Input struct {
	Name        string   `json:"name"`
	Prompt      string   `json:"prompt"`
	Cron        string   `json:"cron"`
	Timezone    string   `json:"timezone"`
	Status      string   `json:"status"`
	Model       *string  `json:"model"`
	SessionMode string   `json:"session_mode"`
	OwnerEmail  *string  `json:"owner_email"`
	EnvFrom     []string `json:"env_from"`
}
type Schedule struct {
	Input
	ID             uuid.UUID  `json:"id"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	NextRunAt      *time.Time `json:"next_run_at"`
	DeletedAt      *time.Time `json:"deleted_at"`
	LastOccurrence *struct{}  `json:"last_occurrence"`
}
type Page struct {
	Items      []Schedule `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}
type Detail struct {
	Path []string `json:"path"`
	Code string   `json:"code"`
}
type Problem struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Phase   *string  `json:"phase"`
	Details []Detail `json:"details"`
}
type Error struct {
	Status  int
	Problem Problem
}

func (e *Error) Error() string { return e.Problem.Message }
func Fail(status int, code, message string) *Error {
	return &Error{Status: status, Problem: Problem{Code: code, Message: message, Details: []Detail{}}}
}
func Invalid(field string) *Error {
	return InvalidAt("body", field)
}
func InvalidAt(path ...string) *Error {
	err := Fail(422, "validation_error", "Request validation failed.")
	err.Problem.Details = []Detail{{Path: path, Code: "invalid_value"}}
	return err
}
func Defaults() Input { return Input{Status: "active", SessionMode: "new", EnvFrom: []string{}} }

func Normalize(in Input, allowed []string, now time.Time) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Cron = strings.Join(strings.Fields(in.Cron), " ")
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 200 {
		return in, Invalid("name")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return in, Invalid("prompt")
	}
	if in.Status != "active" && in.Status != "paused" {
		return in, Invalid("status")
	}
	if in.SessionMode != "new" && in.SessionMode != "reuse" {
		return in, Invalid("session_mode")
	}
	if in.Model != nil {
		value := strings.TrimSpace(*in.Model)
		if value == "" {
			return in, Invalid("model")
		}
		in.Model = &value
	}
	if in.OwnerEmail != nil {
		value, err := Email(*in.OwnerEmail)
		if err != nil {
			return in, err
		}
		in.OwnerEmail = &value
	}
	env, err := EnvNames(in.EnvFrom, allowed)
	if err != nil {
		return in, err
	}
	in.EnvFrom = env
	if _, err = Next(in.Cron, in.Timezone, now); err != nil {
		return in, err
	}
	return in, nil
}

var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func EnvNames(names, allowed []string) ([]string, error) {
	result := append([]string{}, names...)
	slices.Sort(result)
	for i, name := range result {
		if !envPattern.MatchString(name) || i > 0 && result[i-1] == name || allowed != nil && !slices.Contains(allowed, name) {
			return nil, Invalid("env_from")
		}
	}
	return result, nil
}
func Email(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.ContainsAny(value, " \t\r\n") {
		return "", Invalid("owner_email")
	}
	return value, nil
}
func Next(expression, zone string, after time.Time) (time.Time, error) {
	if zone == "" || zone == "Local" {
		return time.Time{}, Invalid("timezone")
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, Invalid("timezone")
	}
	if len(strings.Fields(expression)) != 5 || strings.Contains(expression, "=") {
		return time.Time{}, Invalid("cron")
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	rule, err := parser.Parse(expression)
	if err != nil {
		return time.Time{}, Invalid("cron")
	}
	next := rule.Next(after.In(location))
	if next.IsZero() {
		return next, Invalid("cron")
	}
	return next.UTC(), nil
}
func Patch(current Input, raw []byte) (Input, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return current, Invalid("body")
	}
	for field, value := range fields {
		switch field {
		case "model", "owner_email":
		case "name", "prompt", "cron", "timezone", "status", "session_mode", "env_from":
			if string(value) == "null" {
				return current, Invalid(field)
			}
		default:
			return current, Invalid(field)
		}
	}
	if current.Model != nil {
		current.Model = new(*current.Model)
	}
	if current.OwnerEmail != nil {
		current.OwnerEmail = new(*current.OwnerEmail)
	}
	current.EnvFrom = slices.Clone(current.EnvFrom)
	if err := json.Unmarshal(raw, &current); err != nil {
		return current, Invalid("body")
	}
	return current, nil
}
