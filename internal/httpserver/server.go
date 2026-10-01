// Package httpserver implements Space's generated API contract.
package httpserver

import (
	"context"
	"encoding/json"
	"time"

	"github.com/orpheus-agents/orpheus-space/internal/api"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
)

type Server struct {
	Store  *store.Store
	Config config.Config
	Now    func() time.Time
}

var _ api.StrictServerInterface = (*Server)(nil)

func value[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
func response[T any](source any) (T, error) {
	var out T
	raw, err := json.Marshal(source)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}
func (s *Server) CreateSchedule(ctx context.Context, r api.CreateScheduleRequestObject) (api.CreateScheduleResponseObject, error) {
	if r.Body == nil {
		return nil, schedule.Invalid("body")
	}
	raw, err := json.Marshal(r.Body)
	if err != nil {
		return nil, err
	}
	in := schedule.Defaults()
	if err = json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out, err := s.Store.Create(ctx, in, r.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	return response[api.CreateSchedule201JSONResponse](out)
}
func (s *Server) GetSchedule(ctx context.Context, r api.GetScheduleRequestObject) (api.GetScheduleResponseObject, error) {
	out, err := s.Store.Get(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	return response[api.GetSchedule200JSONResponse](out)
}
func (s *Server) ListSchedules(ctx context.Context, r api.ListSchedulesRequestObject) (api.ListSchedulesResponseObject, error) {
	out, err := s.Store.List(ctx, store.Filter{Owners: value(r.Params.OwnerEmail, []string{}), Unowned: value(r.Params.Unowned, false), Status: string(value(r.Params.Status, ""))}, value(r.Params.Limit, 50), value(r.Params.Cursor, ""))
	if err != nil {
		return nil, err
	}
	return response[api.ListSchedules200JSONResponse](out)
}
func (s *Server) UpdateSchedule(ctx context.Context, r api.UpdateScheduleRequestObject) (api.UpdateScheduleResponseObject, error) {
	if r.Body == nil {
		return nil, schedule.Invalid("body")
	}
	raw, err := json.Marshal(r.Body)
	if err != nil {
		return nil, err
	}
	out, err := s.Store.Update(ctx, r.ID, raw)
	if err != nil {
		return nil, err
	}
	return response[api.UpdateSchedule200JSONResponse](out)
}
func (s *Server) DeleteSchedule(ctx context.Context, r api.DeleteScheduleRequestObject) (api.DeleteScheduleResponseObject, error) {
	if err := s.Store.Delete(ctx, r.ID); err != nil {
		return nil, err
	}
	return api.DeleteSchedule204Response{}, nil
}
func (s *Server) GetSettings(context.Context, api.GetSettingsRequestObject) (api.GetSettingsResponseObject, error) {
	return api.GetSettings200JSONResponse{BaseEnvFrom: append([]string{}, s.Config.Execution.Sandbox.EnvFrom...), AllowedEnvFrom: append([]string{}, s.Config.AllowedEnv...), BrowserAuth: api.SettingsBrowserAuth(s.Config.BrowserAuth)}, nil
}
func (s *Server) PreviewSchedule(_ context.Context, r api.PreviewScheduleRequestObject) (api.PreviewScheduleResponseObject, error) {
	if r.Body == nil {
		return nil, schedule.Invalid("body")
	}
	after := time.Now()
	if s.Now != nil {
		after = s.Now()
	}
	times := make([]time.Time, 0, 5)
	for range 5 {
		next, err := schedule.Next(r.Body.Cron, r.Body.Timezone, after)
		if err != nil {
			return nil, err
		}
		times = append(times, next)
		after = next
	}
	return api.PreviewSchedule200JSONResponse{Times: times}, nil
}
func (s *Server) GetAuthSession(ctx context.Context, _ api.GetAuthSessionRequestObject) (api.GetAuthSessionResponseObject, error) {
	authenticated, _ := ctx.Value(bearerKey{}).(bool)
	access := authenticated || s.Config.BrowserAuth == "anonymous"
	return response[api.GetAuthSession200JSONResponse](map[string]any{"mode": s.Config.BrowserAuth, "authenticated": authenticated, "read_access": access, "write_access": access, "user": nil, "expires_at": nil})
}
