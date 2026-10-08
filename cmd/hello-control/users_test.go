package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

type fakeUsers struct {
	name string
	role auth.Role
	hash string
}

func (f *fakeUsers) CreateUser(_ context.Context, _, username, hash string, role auth.Role) (int64, error) {
	f.name, f.role, f.hash = username, role, hash
	return 3, nil
}

// TestUserAddRole fails if user add does not default to viewer, ignores
// --role, accepts an unknown role or an empty password, or stores the
// password unhashed.
func TestUserAddRole(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		args []string
		want auth.Role
	}{
		{[]string{"sam"}, auth.RoleViewer},
		{[]string{"--role", "operator", "sam"}, auth.RoleOperator},
		{[]string{"--role=admin", "sam"}, auth.RoleAdmin},
	} {
		f := &fakeUsers{}
		if err := userAdd(ctx, f, tc.args, strings.NewReader("pw-123456\n"), io.Discard); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if f.name != "sam" || f.role != tc.want || !auth.CheckPassword(f.hash, "pw-123456") {
			t.Errorf("%v: created %q role %q (password hashed: %v)", tc.args, f.name, f.role, auth.CheckPassword(f.hash, "pw-123456"))
		}
	}
	for _, bad := range [][]string{{"--role", "root", "sam"}, {}, {"a", "b"}} {
		if err := userAdd(ctx, &fakeUsers{}, bad, strings.NewReader("pw\n"), io.Discard); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if err := userAdd(ctx, &fakeUsers{}, []string{"sam"}, strings.NewReader("\n"), io.Discard); err == nil {
		t.Error("empty password accepted")
	}
}
