//go:build integration || live

package testutil

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type Catalog struct{}

func (Catalog) Profiles(_ context.Context, params *coreapi.GetProfilesParams) (coreapi.Profiles, error) {
	items := []coreapi.Profile{}
	for _, name := range []string{"default", "other"} {
		if params != nil && (params.Name != nil && *params.Name != name || params.Active != nil && !*params.Active || params.Available != nil && !*params.Available) {
			continue
		}
		items = append(items, coreapi.Profile{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(name)), Name: name, Harness: coreapi.ProfileHarnessCodex, Model: "gpt-test", AccountID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("account")), AccountName: "Account", Active: true, AccountActive: true, Available: true})
	}
	return coreapi.Profiles{Items: items}, nil
}
func (Catalog) Templates(_ context.Context, params *coreapi.GetTemplatesParams) (coreapi.Templates, error) {
	items := []coreapi.Template{}
	for _, name := range []string{"sandbox", "other"} {
		if params != nil && params.Name != nil && *params.Name != name {
			continue
		}
		if params != nil && params.Active != nil && !*params.Active {
			continue
		}
		items = append(items, coreapi.Template{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(name)), Name: name, TemplateName: name, Active: true})
	}
	return coreapi.Templates{Items: items}, nil
}
func (Catalog) Run(context.Context, uuid.UUID, uuid.UUID) (coreapi.Run, error) {
	return coreapi.Run{}, errors.New("unexpected run request")
}
func Execution() config.Execution {
	var c config.Execution
	c.Agent.Profile = "default"
	c.Sandbox.Template = "sandbox"
	return c
}

func (Catalog) Services(context.Context) (coreapi.Services, error) {
	return coreapi.Services{Items: []coreapi.Service{
		{Code: "a", Name: "Service A", Description: "First test service", EnvNames: []string{"A"}},
		{Code: "b", Name: "Service B", Description: "Second test service", EnvNames: []string{"B"}},
		{Code: "orpheus-space", Name: "Orpheus Space", Description: "Manage schedules", EnvNames: []string{"ORPHEUS_SPACE_API_KEY"}},
		{Code: "gitlab-registry", Name: "GitLab Registry", Description: "Pull images", EnvNames: []string{"GITLAB_REGISTRY_TOKEN"}},
	}}, nil
}
