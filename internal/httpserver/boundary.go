package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orpheus-agents/orpheus-space/internal/api"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

type bearerKey struct{}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	problem, ok := errors.AsType[*schedule.Error](err)
	if !ok {
		cause := err
		for errors.Unwrap(cause) != nil {
			cause = errors.Unwrap(cause)
		}
		attrs := []any{"error_type", fmt.Sprintf("%T", cause), "method", r.Method, "route", r.Pattern}
		if pg, ok := errors.AsType[*pgconn.PgError](err); ok {
			attrs = append(attrs, "sqlstate", pg.Code)
		}
		if r.Context().Err() == nil {
			slog.ErrorContext(r.Context(), "API request failed", attrs...)
		}
		problem = schedule.Fail(503, "storage_unavailable", "Storage is temporarily unavailable.")
	}
	w.Header().Set("Content-Type", "application/json")
	if problem.Status == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(struct {
		Error schedule.Problem `json:"error"`
	}{problem.Problem})
}
func Handler(s *Server) (http.Handler, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, err
	}
	spec.Servers = nil
	router, err := legacy.NewRouter(spec)
	if err != nil {
		return nil, err
	}
	invalid := func(w http.ResponseWriter, r *http.Request, err error) { writeError(w, r, validationProblem(err)) }
	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{RequestErrorHandlerFunc: invalid, ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) { writeError(w, r, err) }})
	generated := api.HandlerWithOptions(strict, api.StdHTTPServerOptions{ErrorHandlerFunc: invalid})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet && r.URL.Path == "/openapi.json" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(spec)
			return
		}
		bearer := false
		if headers, present := r.Header["Authorization"]; present {
			if len(headers) == 1 && strings.HasPrefix(headers[0], "Bearer ") {
				hash := sha256.Sum256([]byte(strings.TrimPrefix(headers[0], "Bearer ")))
				for _, key := range s.Config.PublicAPIKeys {
					candidate := sha256.Sum256([]byte(key))
					bearer = subtle.ConstantTimeCompare(hash[:], candidate[:]) == 1 || bearer
				}
			}
			if !bearer {
				writeError(w, r, schedule.Fail(401, "unauthorized", "Valid API credentials are required."))
				return
			}
		} else if s.Config.BrowserAuth != "anonymous" && r.URL.Path != "/api/v1/auth/session" {
			writeError(w, r, schedule.Fail(401, "unauthorized", "Valid API credentials are required."))
			return
		}
		if !bearer && r.Method != http.MethodGet && r.Method != http.MethodHead {
			if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != s.Config.PublicURL || len(r.Header.Values("X-Orpheus-CSRF")) != 1 || r.Header.Get("X-Orpheus-CSRF") != "1" {
				writeError(w, r, schedule.Fail(403, "csrf_failed", "Origin and CSRF header are required."))
				return
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), bearerKey{}, bearer))
		route, params, err := router.FindRoute(r)
		if err != nil {
			writeError(w, r, schedule.Fail(404, "not_found", "API route not found."))
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, r, schedule.InvalidAt("query"))
			return
		}
		for name, values := range query {
			found := false
			for _, param := range route.Operation.Parameters {
				if param.Value.In == "query" && param.Value.Name == name {
					found = true
					break
				}
			}
			if !found || name != "owner_email" && len(values) > 1 {
				err = schedule.InvalidAt("query", name)
				break
			}
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		if len(r.Header.Values("Idempotency-Key")) > 1 {
			writeError(w, r, schedule.InvalidAt("header", "Idempotency-Key"))
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.Config.MaxRequestBytes))
			if err != nil {
				if _, large := errors.AsType[*http.MaxBytesError](err); large {
					writeError(w, r, schedule.Fail(413, "request_too_large", "Request body is too large."))
				} else {
					writeError(w, r, schedule.InvalidAt("body"))
				}
				return
			}
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || media != "application/json" {
				writeError(w, r, schedule.Fail(415, "unsupported_media_type", "Use application/json."))
				return
			}
			if validateJSON(raw) != nil {
				writeError(w, r, schedule.InvalidAt("body"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		validation := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route, Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, SkipSettingDefaults: true}}
		if err := openapi3filter.ValidateRequest(r.Context(), validation); err != nil {
			invalid(w, r, err)
			return
		}
		generated.ServeHTTP(w, r)
	}), nil
}

func validationProblem(err error) *schedule.Error {
	p := schedule.Fail(422, "validation_error", "Request validation failed.")
	detail := schedule.Detail{Path: []string{"body"}, Code: "invalid_value"}
	// Generated decoding can reject integers that schema validation accepted
	// after float64 rounding. Keep the field path without exposing its value.
	if decoded, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && decoded.Field != "" {
		for segment := range strings.SplitSeq(decoded.Field, ".") {
			detail.Path = append(detail.Path, segment)
		}
	}
	if param, ok := errors.AsType[*api.InvalidParamFormatError](err); ok {
		location := "query"
		switch param.ParamName {
		case "id":
			location = "path"
		case "Idempotency-Key":
			location = "header"
		}
		detail.Path = []string{location, param.ParamName}
	}
	if request, ok := errors.AsType[*openapi3filter.RequestError](err); ok && request.Parameter != nil {
		detail.Path = []string{request.Parameter.In, request.Parameter.Name}
		if errors.Is(err, openapi3filter.ErrInvalidRequired) {
			detail.Code = "required"
		}
	}
	if schema, ok := errors.AsType[*openapi3.SchemaError](err); ok {
		detail.Path = append(detail.Path, schema.JSONPointer()...)
		switch schema.SchemaField {
		case "type":
			detail.Code = "invalid_type"
		case "required":
			detail.Code = "required"
		case "additionalProperties":
			detail.Code = "unknown_field"
		case "properties":
			// kin-openapi reports forbidden properties at their parent object.
			// Find the first unknown key without parsing or exposing error text.
			if object, ok := schema.Value.(map[string]any); ok && schema.Schema != nil {
				for _, key := range slices.Sorted(maps.Keys(object)) {
					if _, known := schema.Schema.Properties[key]; !known {
						detail.Path = append(detail.Path, key)
						detail.Code = "unknown_field"
						break
					}
				}
			}
		}
	}
	p.Problem.Details = []schedule.Detail{detail}
	return p
}

func validateJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 256 {
			return errors.New("JSON nesting limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("duplicate JSON key")
				}
				keys[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}
