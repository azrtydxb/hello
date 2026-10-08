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

// userAdd runs "user add [--role viewer|operator|admin] <username>": it
// reads the password from the first line of stdin and creates the user
// with the role (default viewer; the first user is always admin).
func userAdd(ctx context.Context, st userCreator, args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	role := fs.String("role", string(auth.RoleViewer), "viewer, operator or admin")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("user add: %w", err)
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return errors.New("usage: hello-control user add [--role viewer|operator|admin] <username> < password")
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
	hash, err := auth.HashPassword(password)
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
