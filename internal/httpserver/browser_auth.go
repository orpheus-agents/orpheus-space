package httpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/api"
)

type browserRequestKey struct{}

// Only auth visitors need the raw HTTP request for cookies and SAML forms.
func browserRequestMiddleware(next api.StrictHandlerFunc, operation string) api.StrictHandlerFunc {
	switch operation {
	case "GetAuthSession", "BrowserLogin", "BrowserCallback", "BrowserLogout", "SamlMetadata":
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			return next(context.WithValue(ctx, browserRequestKey{}, r), w, r, request)
		}
	default:
		return next
	}
}

// The custom response visitor preserves Set-Cookie, redirects and the SAML form
// protocol while all routes and response types remain generated from OpenAPI.
type browserResponse struct {
	serve func(http.ResponseWriter) error
}

func (b browserResponse) VisitGetAuthSessionResponse(w http.ResponseWriter) error  { return b.serve(w) }
func (b browserResponse) VisitBrowserLoginResponse(w http.ResponseWriter) error    { return b.serve(w) }
func (b browserResponse) VisitBrowserCallbackResponse(w http.ResponseWriter) error { return b.serve(w) }
func (b browserResponse) VisitBrowserLogoutResponse(w http.ResponseWriter) error   { return b.serve(w) }
func (b browserResponse) VisitSamlMetadataResponse(w http.ResponseWriter) error    { return b.serve(w) }
func (s *Server) browserResponse(ctx context.Context, handle func(http.ResponseWriter, *http.Request) error) browserResponse {
	return browserResponse{serve: func(w http.ResponseWriter) error {
		r := ctx.Value(browserRequestKey{}).(*http.Request)
		// Keep the raw request/body, with the current handler context.
		return handle(w, r.WithContext(ctx))
	}}
}
func (s *Server) GetAuthSession(ctx context.Context, _ api.GetAuthSessionRequestObject) (api.GetAuthSessionResponseObject, error) {
	return s.browserResponse(ctx, func(w http.ResponseWriter, r *http.Request) error {
		bearer, _ := ctx.Value(bearerKey{}).(bool)
		if bearer {
			state := api.GetAuthSession200JSONResponse{Mode: api.AuthSessionMode(s.Config.Auth.Mode), Authenticated: true, ReadAccess: true, WriteAccess: true, CanManageAll: true}
			return state.VisitGetAuthSessionResponse(w)
		}
		state, err := s.auth.State(w, r)
		if err != nil {
			return err
		}
		if state.User != nil {
			actor := access.Browser(state.User.Email, s.Config.Access.AdminEmails)
			state.WriteAccess = actor.CanCreate()
			state.CanManageAll = actor.ManageAll
		}
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(state)
	}), nil
}
func (s *Server) BrowserLogin(ctx context.Context, _ api.BrowserLoginRequestObject) (api.BrowserLoginResponseObject, error) {
	return s.browserResponse(ctx, s.auth.Login), nil
}
func (s *Server) BrowserCallback(ctx context.Context, _ api.BrowserCallbackRequestObject) (api.BrowserCallbackResponseObject, error) {
	return s.browserResponse(ctx, s.auth.Callback), nil
}
func (s *Server) BrowserLogout(ctx context.Context, _ api.BrowserLogoutRequestObject) (api.BrowserLogoutResponseObject, error) {
	return s.browserResponse(ctx, s.auth.Logout), nil
}
func (s *Server) SamlMetadata(ctx context.Context, _ api.SamlMetadataRequestObject) (api.SamlMetadataResponseObject, error) {
	return s.browserResponse(ctx, s.auth.Metadata), nil
}
