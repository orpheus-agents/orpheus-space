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

func (Catalog) Profiles(context.Context) (coreapi.Profiles, error) {
	return coreapi.Profiles{Items: []coreapi.Profile{{Name: "default", Harness: coreapi.Codex}, {Name: "other", Harness: coreapi.Codex}}}, nil
}
func (Catalog) Templates(context.Context) (coreapi.Templates, error) {
	return coreapi.Templates{Items: []coreapi.Template{{Name: "sandbox"}, {Name: "other"}}}, nil
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
