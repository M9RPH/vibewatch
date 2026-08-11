package app

import (
	"testing"

	"github.com/watchtower-ui/watchtower-ui/internal/auth"
	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

func TestManagedUserVisibilityHierarchy(t *testing.T) {
	viewer := auth.Identity{UserID: 7, Username: "test2", Role: "admin"}
	cases := []struct {
		u     db.User
		owner bool
		want  bool
	}{
		{db.User{ID: 7, Role: "admin"}, false, true},
		{db.User{ID: 8, Role: "admin"}, false, false},
		{db.User{ID: 9, Role: "user"}, false, true},
		{db.User{ID: 8, Role: "admin"}, true, true},
	}
	for _, tc := range cases {
		if got := managedUserVisible(viewer, tc.owner, tc.u); got != tc.want {
			t.Fatalf("user=%d role=%s owner=%v got=%v want=%v", tc.u.ID, tc.u.Role, tc.owner, got, tc.want)
		}
	}
}
