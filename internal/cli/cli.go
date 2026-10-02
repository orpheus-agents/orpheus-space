// Package cli implements the noninteractive Space command line interface.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	api "github.com/orpheus-agents/orpheus-space/client"
	"github.com/spf13/cobra"
)

const maxBody = 1024 * 1024

// Execute keeps credentials out of arguments, diagnostics and redirects.
func Execute(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer, getenv func(string) string, version string) int {
	root := command(input, output, getenv, version)
	root.SetArgs(args)
	root.SetOut(output)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		// Cobra's diagnostics may quote user input. Only our own safe errors escape.
		message := "invalid command or arguments; see --help"
		if safe, ok := errors.AsType[*failure](err); ok {
			message = safe.message
		}
		_ = json.NewEncoder(stderr).Encode(map[string]any{"error": message})
		return 1
	}
	return 0
}

type failure struct{ message string }

func (e *failure) Error() string { return e.message }
func fail(message string) error  { return &failure{message} }

type options struct {
	host, key string
	json      bool
	input     io.Reader
	output    io.Writer
}

func command(input io.Reader, output io.Writer, getenv func(string) string, version string) *cobra.Command {
	o := &options{host: getenv("ORPHEUS_SPACE_HOST"), key: getenv("ORPHEUS_SPACE_API_KEY"), input: input, output: output}
	root := &cobra.Command{Use: "orpheus-space", Short: "Manage Orpheus Space schedules", Version: version, SilenceErrors: true, SilenceUsage: true}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetVersionTemplate("orpheus-space {{.Version}}\n")
	root.PersistentFlags().StringVar(&o.host, "host", o.host, "Space origin (ORPHEUS_SPACE_HOST); no /api/v1 path")
	root.PersistentFlags().Lookup("host").DefValue = "" // Do not echo environment configuration in help.
	root.PersistentFlags().BoolVar(&o.json, "json", false, "Emit compact JSON (default: indented JSON)")
	group := &cobra.Command{Use: "schedule", Short: "Manage schedules; key comes from ORPHEUS_SPACE_API_KEY", Long: "Manage schedules through the Space API. Schedule JSON includes url, the public web card link, or null when the server has no public URL configured."}
	root.AddCommand(group)
	for _, name := range []string{"list", "history"} {
		var owners []string
		var unowned bool
		var status, cursor string
		var limit int
		use, nargs := name, 0
		if name == "history" {
			use += " <id>"
			nargs = 1
		}
		cmd := &cobra.Command{Use: use, Short: "Read one page from Space's database; pass next_cursor to continue", Args: cobra.ExactArgs(nargs)}
		cmd.Flags().IntVar(&limit, "limit", 50, "Page size, 1..200")
		cmd.Flags().StringVar(&cursor, "cursor", "", "Opaque cursor from the previous page")
		if name == "list" {
			cmd.Flags().StringArrayVar(&owners, "owner-email", nil, "Owner email; repeat for OR filtering")
			cmd.Flags().BoolVar(&unowned, "unowned", false, "Only shared schedules; incompatible with --owner-email")
			cmd.Flags().StringVar(&status, "status", "", "active or paused")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if limit < 1 || limit > 200 || unowned && len(owners) > 0 || status != "" && status != "active" && status != "paused" {
				return fail("invalid list filters or limit")
			}
			id, err := parseID(args, 0)
			if err != nil {
				return err
			}
			return o.call(func(c *api.Client) (*http.Response, error) {
				if name == "history" {
					return c.ListOccurrences(cmd.Context(), id, &api.ListOccurrencesParams{Limit: &limit, Cursor: optional(cursor)})
				}
				params := &api.ListSchedulesParams{Limit: &limit, Cursor: optional(cursor), Unowned: &unowned}
				if len(owners) > 0 {
					params.OwnerEmail = &owners
				}
				if status != "" {
					params.Status = new(api.Status(status))
				}
				return c.ListSchedules(cmd.Context(), params)
			})
		}
		group.AddCommand(cmd)
	}
	for _, name := range []string{"get", "delete", "pause", "resume", "reset-session", "occurrence", "result", "settings"} {
		nargs := 1
		short := "Read or change a schedule in Space's database"
		use := name + " <id>"
		if name == "settings" {
			nargs = 0
			use = name
		}
		if name == "occurrence" || name == "result" {
			nargs = 2
			use += " <occurrence-id>"
		}
		if name == "result" {
			short = "Explicitly read the run result from Orpheus core through Space; requires core availability"
		}
		cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(nargs)}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args, 0)
			if err != nil {
				return err
			}
			oid, err := parseID(args, 1)
			if err != nil {
				return err
			}
			return o.call(func(c *api.Client) (*http.Response, error) {
				ctx := cmd.Context()
				switch name {
				case "settings":
					return c.GetSettings(ctx)
				case "get":
					return c.GetSchedule(ctx, id)
				case "delete":
					return c.DeleteSchedule(ctx, id)
				case "reset-session":
					return c.ResetSession(ctx, id)
				case "occurrence":
					return c.GetOccurrence(ctx, id, oid)
				case "result":
					return c.GetOccurrenceResult(ctx, id, oid)
				default:
					status := "paused"
					if name == "resume" {
						status = "active"
					}
					return c.UpdateScheduleWithBody(ctx, id, "application/json", strings.NewReader(`{"status":"`+status+`"}`))
				}
			})
		}
		group.AddCommand(cmd)
	}
	for _, name := range []string{"create", "update"} {
		var file, key string
		use, nargs := name, 0
		if name == "update" {
			use += " <id>"
			nargs = 1
		}
		cmd := &cobra.Command{Use: use + " --file <path|->", Short: "Send a JSON object from a file or stdin", Args: cobra.ExactArgs(nargs)}
		cmd.Flags().StringVar(&file, "file", "", "JSON file, or - for stdin (required)")
		if name == "create" {
			cmd.Flags().StringVar(&key, "idempotency-key", "", "UUID for safe create retries; generated when omitted")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args, 0)
			if err != nil {
				return err
			}
			body, err := readBody(file, o.input)
			if err != nil {
				return err
			}
			if name == "create" {
				if key == "" {
					key = uuid.NewString()
				}
				parsed, err := uuid.Parse(key)
				if err != nil || parsed == uuid.Nil {
					return fail("invalid idempotency key: expected a nonzero UUID")
				}
				key = parsed.String()
			}
			err = o.call(func(c *api.Client) (*http.Response, error) {
				if name == "create" {
					return c.CreateScheduleWithBody(cmd.Context(), &api.CreateScheduleParams{IdempotencyKey: new(uuid.MustParse(key))}, "application/json", bytes.NewReader(body))
				}
				return c.UpdateScheduleWithBody(cmd.Context(), id, "application/json", bytes.NewReader(body))
			})
			if err != nil && name == "create" {
				return fail(err.Error() + "; retry the same input with --idempotency-key " + key)
			}
			return err
		}
		group.AddCommand(cmd)
	}
	var cron, zone string
	preview := &cobra.Command{Use: "preview --cron <expression> --timezone <IANA zone>", Short: "Preview the next five scheduled times", Args: cobra.NoArgs}
	preview.Flags().StringVar(&cron, "cron", "", "Five-field cron")
	preview.Flags().StringVar(&zone, "timezone", "", "IANA timezone")
	preview.RunE = func(cmd *cobra.Command, _ []string) error {
		if cron == "" || zone == "" {
			return fail("--cron and --timezone are required")
		}
		return o.call(func(c *api.Client) (*http.Response, error) {
			return c.PreviewSchedule(cmd.Context(), api.PreviewInput{Cron: cron, Timezone: zone})
		})
	}
	group.AddCommand(preview)
	return root
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func parseID(args []string, index int) (uuid.UUID, error) {
	if index >= len(args) {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(args[index])
	if err != nil || id == uuid.Nil {
		return id, fail("invalid ID: expected a nonzero UUID")
	}
	return id, nil
}
func readBody(file string, stdin io.Reader) ([]byte, error) {
	if file == "" {
		return nil, fail("--file is required")
	}
	reader := stdin
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return nil, fail("cannot open JSON input file")
		}
		defer func() { _ = f.Close() }()
		reader = f
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBody+1))
	if err != nil {
		return nil, fail("cannot read JSON input")
	}
	if len(body) > maxBody {
		return nil, fail("JSON input exceeds 1 MiB")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil || obj == nil {
		return nil, fail("input must be one JSON object")
	}
	return body, nil
}
func (o *options) call(send func(*api.Client) (*http.Response, error)) error {
	u, err := url.Parse(o.host)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fail("ORPHEUS_SPACE_HOST or --host must be an HTTP(S) origin")
	}
	if strings.TrimSpace(o.key) == "" {
		return fail("ORPHEUS_SPACE_API_KEY is required")
	}
	c, err := api.NewClient(o.host, api.WithHTTPClient(&http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}), api.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+o.key)
		return nil
	}))
	if err != nil {
		return fail("cannot configure API client")
	}
	res, err := send(c)
	if err != nil {
		return fail("Space request failed; outcome may be unknown")
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8*maxBody+1))
	if err != nil || len(raw) > 8*maxBody {
		return fail("invalid or oversized Space response; outcome may be unknown")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := "request_failed"
		var envelope api.Problem
		if json.Unmarshal(raw, &envelope) == nil {
			switch envelope.Error.Code {
			case "validation_error", "unauthorized", "forbidden", "schedule_not_found", "occurrence_not_found", "invalid_cursor", "idempotency_conflict", "schedule_busy", "schedule_deleted", "run_not_found", "session_not_found", "core_unavailable", "occurrence_not_started", "request_too_large":
				code = envelope.Error.Code
			}
		}
		return fail(fmt.Sprintf("Space HTTP %d: %s", res.StatusCode, code))
	}
	if res.StatusCode == http.StatusNoContent {
		raw = []byte(`{"ok":true}`)
	}
	if !json.Valid(raw) {
		return fail("invalid Space JSON response; outcome may be unknown")
	}
	encoder := json.NewEncoder(o.output)
	if !o.json {
		encoder.SetIndent("", "  ")
	}
	if encoder.Encode(json.RawMessage(raw)) != nil {
		return fail("cannot write result; request may have succeeded")
	}
	return nil
}
