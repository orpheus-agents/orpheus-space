package access

import (
	"testing"
)

func TestPermissions(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		actor                      Principal
		create, own, other, shared bool
	}{
		{"absent", Principal{}, false, false, false, false},
		{"empty", Browser(new(""), nil), false, false, false, false},
		{"user", Browser(new("alice@example.com"), nil), true, true, false, false},
		{"alias", Browser(new("alice+work@example.com"), []string{"alice@example.com"}), true, false, false, false},
		{"admin", Browser(new("alice@example.com"), []string{"alice@example.com"}), true, true, true, true},
		{"service", Principal{ManageAll: true}, true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actor.CanCreate() != tc.create || tc.actor.CanManage(new("alice@example.com")) != tc.own || tc.actor.CanManage(new("bob@example.com")) != tc.other || tc.actor.CanManage(nil) != tc.shared {
				t.Fatal("incorrect permissions", tc.actor)
			}
			if (tc.actor.RequireOwner(new(" ALICE@EXAMPLE.COM ")) == nil) != tc.own || (tc.actor.RequireOwner(nil) == nil) != tc.shared {
				t.Fatal("incorrect requested owner permission")
			}
		})
	}
}
