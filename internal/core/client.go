// Package core provides the narrow Space integration with the pinned core client.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type Failure struct {
	Code      string
	Status    int
	Uncertain bool
	Details   []schedule.Detail
}

func (e *Failure) Error() string { return e.Code }

type Client struct{ api *coreapi.Client }

func New(host, key string) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = 16
	c, err := coreapi.NewClient(host, coreapi.WithHTTPClient(&http.Client{Timeout: 20 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}), coreapi.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+key)
		return nil
	}))
	return &Client{api: c}, err
}

// decode limits upstream bodies and never returns upstream error text.
func decode(res *http.Response, err error, out any) error {
	if err != nil {
		return &Failure{Code: "core_unavailable", Uncertain: true}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024+1))
	if err != nil || len(raw) > 8*1024*1024 {
		return &Failure{Code: "core_invalid_response", Uncertain: true}
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if json.Unmarshal(raw, out) != nil {
			return &Failure{Code: "core_invalid_response", Uncertain: true}
		}
		return nil
	}
	code := "core_unavailable"
	var details []schedule.Detail
	var envelope coreapi.ErrorResponse
	if json.Unmarshal(raw, &envelope) == nil {
		switch envelope.Error.Code {
		case "unknown_profile", "unknown_template", "unknown_service", "capacity_exhausted", "session_busy", "idempotency_conflict", "validation_error", "invalid_cursor", "unauthorized", "session_not_found", "run_not_found", "multiple_runs_not_allowed", "token_limit_exceeded", "session_unavailable", "storage_unavailable", "request_too_large", "unsupported_media_type", "invalid_json", "idempotency_key_required":
			code = envelope.Error.Code
		}
		if res.StatusCode == 422 && (code == "invalid_cursor" || code == "validation_error") {
			details = catalogQueryDetails(envelope.Error.Details)
		}
	}
	return &Failure{Code: code, Status: res.StatusCode, Details: details, Uncertain: res.StatusCode >= 500 && code != "capacity_exhausted" || res.StatusCode >= 300 && res.StatusCode < 400}
}

// Only fixed query names and validation codes cross the catalog proxy boundary.
func catalogQueryDetails(in []coreapi.ErrorDetail) []schedule.Detail {
	out := []schedule.Detail{}
	for _, d := range in {
		if len(d.Path) != 2 {
			continue
		}
		location, err := d.Path[0].AsErrorDetailPath0()
		if err != nil || location != "query" {
			continue
		}
		field, err := d.Path[1].AsErrorDetailPath0()
		if err != nil {
			continue
		}
		switch field {
		case "active", "name", "name_contains", "limit", "cursor":
		default:
			continue
		}
		switch string(d.Code) {
		case "required", "invalid_type", "invalid_value", "unknown_field":
		default:
			continue
		}
		out = append(out, schedule.Detail{Path: []string{"query", field}, Code: string(d.Code)})
	}
	return out
}
func (c *Client) Dispatch(ctx context.Context, path string, body []byte, key uuid.UUID) (coreapi.Accepted, error) {
	var accepted coreapi.Accepted
	var res *http.Response
	defer func() {
		if res != nil {
			_ = res.Body.Close()
		}
	}()
	var err error
	if path == "/api/v1/sessions" {
		res, err = c.api.CreateSessionWithBody(ctx, &coreapi.CreateSessionParams{IdempotencyKey: new(key.String())}, "application/json", bytes.NewReader(body))
	} else {
		raw, ok := strings.CutPrefix(path, "/api/v1/sessions/")
		id, tail, found := strings.Cut(raw, "/")
		sid, parseErr := uuid.Parse(id)
		if !ok || !found || tail != "runs" || parseErr != nil {
			return accepted, &Failure{Code: "invalid_snapshot", Uncertain: true}
		}
		res, err = c.api.CreateRunWithBody(ctx, sid, &coreapi.CreateRunParams{IdempotencyKey: new(key.String())}, "application/json", bytes.NewReader(body))
	}
	err = decode(res, err, &accepted)
	if err == nil && (accepted.SessionID == uuid.Nil || accepted.RunID == uuid.Nil) {
		err = &Failure{Code: "core_invalid_response", Uncertain: true}
	}
	return accepted, err
}
func (c *Client) Run(ctx context.Context, sid, rid uuid.UUID) (coreapi.Run, error) {
	var run coreapi.Run
	res, err := c.api.GetRun(ctx, sid, rid)
	if res != nil {
		defer func() { _ = res.Body.Close() }()
	}
	err = decode(res, err, &run)
	if err == nil && (run.ID != rid || run.SessionID != sid || !run.Status.Valid()) {
		err = &Failure{Code: "core_invalid_response", Uncertain: true}
	}
	return run, err
}
func (c *Client) ActiveRun(ctx context.Context, sid uuid.UUID) (*uuid.UUID, error) {
	var session coreapi.Session
	res, err := c.api.GetSession(ctx, sid)
	if res != nil {
		defer func() { _ = res.Body.Close() }()
	}
	if err = decode(res, err, &session); err != nil {
		return nil, err
	}
	if session.ID != sid {
		return nil, &Failure{Code: "core_invalid_response", Uncertain: true}
	}
	return session.ActiveRunID, nil
}
func ErrorCode(err error) string {
	if failure, ok := errors.AsType[*Failure](err); ok {
		return failure.Code
	}
	return "core_unavailable"
}
func Terminal(status coreapi.RunStatus) bool {
	return status == coreapi.RunStatusCompleted || status == coreapi.RunStatusFailed || status == coreapi.RunStatusCancelled
}

func (c *Client) Profiles(ctx context.Context) (coreapi.Profiles, error) {
	var out coreapi.Profiles
	res, err := c.api.GetProfiles(ctx)
	if res != nil {
		defer func() { _ = res.Body.Close() }()
	}
	err = decode(res, err, &out)
	if err == nil && out.Items == nil {
		err = &Failure{Code: "core_invalid_response"}
	}
	return out, err
}
func (c *Client) Templates(ctx context.Context, params *coreapi.GetTemplatesParams) (coreapi.Templates, error) {
	var out coreapi.Templates
	res, err := c.api.GetTemplates(ctx, params)
	if res != nil {
		defer func() { _ = res.Body.Close() }()
	}
	var raw json.RawMessage
	if err = decode(res, err, &raw); err != nil {
		return out, err
	}
	if res.StatusCode != http.StatusOK {
		return coreapi.Templates{}, &Failure{Code: "core_invalid_response"}
	}
	return decodeTemplatePage(raw)
}

func (c *Client) Services(ctx context.Context) (coreapi.Services, error) {
	var out coreapi.Services
	res, err := c.api.GetServices(ctx)
	if res != nil {
		defer func() { _ = res.Body.Close() }()
	}
	err = decode(res, err, &out)
	if err == nil && out.Items == nil {
		err = &Failure{Code: "core_invalid_response"}
	}
	return out, err
}
