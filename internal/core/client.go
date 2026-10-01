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
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type Failure struct {
	Code      string
	Status    int
	Uncertain bool
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
	var envelope coreapi.ErrorResponse
	if json.Unmarshal(raw, &envelope) == nil {
		switch envelope.Error.Code {
		case "capacity_exhausted", "session_busy", "idempotency_conflict", "validation_error", "unauthorized", "session_not_found", "run_not_found", "multiple_runs_not_allowed", "token_limit_exceeded", "session_unavailable", "storage_unavailable", "request_too_large", "unsupported_media_type", "invalid_json", "idempotency_key_required":
			code = envelope.Error.Code
		}
	}
	return &Failure{Code: code, Status: res.StatusCode, Uncertain: res.StatusCode >= 500 && code != "capacity_exhausted" || res.StatusCode >= 300 && res.StatusCode < 400}
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
