package store

import (
	"context"
	"slices"

	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type Catalog interface {
	Profiles(context.Context) (coreapi.Profiles, error)
	Templates(context.Context) (coreapi.Templates, error)
	Services(context.Context) (coreapi.Services, error)
}

// Empty arguments mean an unchanged selection in PATCH.
func (s *Store) validateSelection(ctx context.Context, profile, template string, services []string) error {
	if profile == "" && template == "" && len(services) == 0 {
		return nil
	}
	unavailable := schedule.Fail(503, "core_unavailable", "Orpheus is temporarily unavailable.")
	if s.Catalog == nil {
		return unavailable
	}
	if profile != "" {
		profiles, err := s.Catalog.Profiles(ctx)
		if err != nil {
			return unavailable
		}
		if !slices.ContainsFunc(profiles.Items, func(p coreapi.Profile) bool { return p.Name == profile }) {
			return schedule.Invalid("profile")
		}
	}
	if template != "" {
		templates, err := s.Catalog.Templates(ctx)
		if err != nil {
			return unavailable
		}
		if !slices.ContainsFunc(templates.Items, func(t coreapi.Template) bool { return t.Name == template }) {
			return schedule.Invalid("template")
		}
	}
	if len(services) > 0 {
		catalog, err := s.Catalog.Services(ctx)
		if err != nil {
			return unavailable
		}
		for _, code := range services {
			if !slices.ContainsFunc(catalog.Items, func(service coreapi.Service) bool { return service.Code == code }) {
				return schedule.Invalid("services")
			}
		}
	}
	return nil
}
