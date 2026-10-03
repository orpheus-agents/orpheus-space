// Package access defines schedule management permissions independently of HTTP.
package access

import (
	"slices"

	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

// Principal is explicit for every store mutation. Its zero value cannot write.
// Email and administrator addresses must use schedule.Email normalization.
type Principal struct {
	ManageAll bool
	Email     *string
}

func Browser(email *string, admins []string) Principal {
	return Principal{Email: email, ManageAll: email != nil && slices.Contains(admins, *email)}
}

func (p Principal) CanCreate() bool { return p.ManageAll || p.Email != nil && *p.Email != "" }

func (p Principal) CanManage(owner *string) bool {
	return p.ManageAll || p.Email != nil && *p.Email != "" && owner != nil && *owner == *p.Email
}

func (p Principal) RequireManage(owner *string) error {
	if !p.CanManage(owner) {
		return schedule.Fail(403, "schedule_forbidden", "You do not have permission to change this schedule or its owner.")
	}
	return nil
}

// RequireOwner checks a requested owner before persistence or catalog requests.
func (p Principal) RequireOwner(owner *string) error {
	if p.ManageAll || owner == nil {
		return p.RequireManage(owner)
	}
	email, err := schedule.Email(*owner)
	if err != nil {
		return err
	}
	return p.RequireManage(&email)
}
