package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/config"
)

func withAccounts(ctx context.Context, cfg config.Config, f func(*accounts.Service) error) error {
	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	return f(accounts.New(db))
}

func userCmd(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	if len(args) == 0 {
		return usageError("user: missing subcommand")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "add":
		fs := flag.NewFlagSet("user add", flag.ContinueOnError)
		role := fs.String("role", string(accounts.RoleUser), "role: admin or user")
		name, err := oneArg(fs, args, "user add <name>")
		if err != nil {
			return err
		}
		return withAccounts(ctx, cfg, func(s *accounts.Service) error {
			if _, err := s.CreateUser(ctx, name, accounts.Role(*role)); err != nil {
				return err
			}
			fmt.Fprintf(out, "user %q created (%s)\n", name, *role)
			return nil
		})
	case "list":
		if len(args) != 0 {
			return usageError("usage: user list")
		}
		return withAccounts(ctx, cfg, func(s *accounts.Service) error {
			users, err := s.ListUsers(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "USERNAME\tROLE\tCREATED\tDISABLED")
			for _, u := range users {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.Username, u.Role, fmtTime(&u.CreatedAt), fmtTime(u.DisabledAt))
			}
			return tw.Flush()
		})
	case "disable":
		name, err := oneArg(flag.NewFlagSet("user disable", flag.ContinueOnError), args, "user disable <name>")
		if err != nil {
			return err
		}
		return withAccounts(ctx, cfg, func(s *accounts.Service) error {
			if err := s.DisableUser(ctx, name); err != nil {
				return fmt.Errorf("user %q: %w", name, err)
			}
			fmt.Fprintf(out, "user %q disabled\n", name)
			return nil
		})
	case "delete":
		name, err := oneArg(flag.NewFlagSet("user delete", flag.ContinueOnError), args, "user delete <name>")
		if err != nil {
			return err
		}
		return withAccounts(ctx, cfg, func(s *accounts.Service) error {
			if err := s.DeleteUser(ctx, name); err != nil {
				return fmt.Errorf("user %q: %w", name, err)
			}
			fmt.Fprintf(out, "user %q deleted with devices and reading progress\n", name)
			return nil
		})
	}
	return usageError(fmt.Sprintf("user: unknown subcommand %q", sub))
}

func deviceCmd(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	if len(args) == 0 {
		return usageError("device: missing subcommand")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "add":
		if len(args) != 2 {
			return usageError("usage: device add <user> <device>")
		}
		user, name := args[0], args[1]
		return withAccounts(ctx, cfg, func(s *accounts.Service) error {
			pass, err := s.AddDevice(ctx, user, name)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "device %q added for %q\n\n  username: %s\n  password: %s\n\n"+
				"The password is shown only once. Enter it in KOReader: Progress sync → Register / Login.\n",
				name, user, user, pass)
			return nil
		})
	case "list":
		user, err := oneArg(flag.NewFlagSet("device list", flag.ContinueOnError), args, "device list <user>")
		if err != nil {
			return err
		}
		return withAccounts(ctx, cfg, func(s *accounts.Service) error {
			devs, err := s.ListDevices(ctx, user)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "DEVICE\tCREATED\tLAST USED\tREVOKED")
			for _, d := range devs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.Name, fmtTime(&d.CreatedAt), fmtTime(d.LastUsedAt), fmtTime(d.RevokedAt))
			}
			return tw.Flush()
		})
	case "revoke":
		if len(args) != 2 {
			return usageError("usage: device revoke <user> <device>")
		}
		user, name := args[0], args[1]
		return withAccounts(ctx, cfg, func(s *accounts.Service) error {
			if err := s.RevokeDevice(ctx, user, name); err != nil {
				return fmt.Errorf("device %q of %q: %w", name, user, err)
			}
			fmt.Fprintf(out, "device %q of %q revoked\n", name, user)
			return nil
		})
	}
	return usageError(fmt.Sprintf("device: unknown subcommand %q", sub))
}

// oneArg разбирает флаги и требует ровно один позиционный аргумент.
// Флаги допускаются и после аргумента: `user add alice -role admin`.
func oneArg(fs *flag.FlagSet, args []string, usage string) (string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", usageError("usage: " + usage + ": " + err.Error())
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 1 {
		return "", usageError("usage: " + usage)
	}
	return pos[0], nil
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}
