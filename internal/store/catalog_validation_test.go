package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

func TestInvalidTemplateSelectionDoesNotNeedCore(t *testing.T) {
	storage := &Store{}
	for _, name := range []string{"a\x00", string([]byte{'a', 0xff})} {
		err := storage.validateSelection(t.Context(), "default", name, nil)
		problem, ok := errors.AsType[*schedule.Error](err)
		if !ok || problem.Status != 422 || len(problem.Problem.Details) != 1 || strings.Join(problem.Problem.Details[0].Path, ".") != "body.template" {
			t.Fatal(err)
		}
	}
}
