package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
)

// userCreator is the store method user add needs.
type userCreator interface {
	CreateUser(ctx context.Context, actor, username, passwordHash string, role auth.Role) (int64, error)
}

// userAdd runs "user add [--role user|viewer|operator|admin] <username>": it
// reads the password from the first line of stdin and creates the user
// with the role (default viewer; the first user is always admin).
func userAdd(ctx context.Context, st userCreator, args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	role := fs.String("role", string(auth.RoleViewer), "user, viewer, operator or admin")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("user add: %w", err)
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return errors.New("usage: hello-control user add [--role user|viewer|operator|admin] <username> < password")
	}
	r, err := auth.ParseRole(*role)
	if err != nil {
		return fmt.Errorf("user add: %w", err)
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("user add: read password: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return errors.New("user add: the password is read from stdin and must not be empty")
	}
	hash, err := auth.HashUserPassword(password)
	if err != nil {
		return err
	}
	id, err := st.CreateUser(ctx, "cli", fs.Arg(0), hash, r)
	if err != nil {
		return fmt.Errorf("user add: %w", err)
	}
	_, _ = fmt.Fprintf(out, "user %s created (id %d)\n", fs.Arg(0), id)
	return nil
}

// twoFactorResetter is the store method user 2fa-reset needs.
type twoFactorResetter interface {
	ResetTOTPByName(ctx context.Context, actor, username string) error
}

// user2FAReset runs "user 2fa-reset <username>", the break-glass way to clear
// a user's 2FA (spec pbx-gaps S-12). It needs shell access to the control
// plane and has none of the API's lockout guard. The user's sessions end.
func user2FAReset(ctx context.Context, st twoFactorResetter, username string, out io.Writer) error {
	if strings.TrimSpace(username) == "" {
		return errors.New("usage: hello-control user 2fa-reset <username>")
	}
	if err := st.ResetTOTPByName(ctx, "cli", username); err != nil {
		return fmt.Errorf("user 2fa-reset: %w", err)
	}
	_, _ = fmt.Fprintf(out, "two-factor authentication cleared for %s\n", username)
	return nil
}
