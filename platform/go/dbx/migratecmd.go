package dbx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateCommand is the first argument that runs a service binary as its own schema migration step instead of the
// service: `<binary> migrate`. Every service chart with migrations runs it from the owner-role migration Job
// (templates/migrate-job.yaml, an Argo CD PreSync hook; ADR-0012 rule 3, D28), so serving pods keep MIGRATE_ON_START=false
// and connect as depot_app only.
const MigrateCommand = "migrate"

// MigrateHook runs after the migrations, on the same owner-role pool (org seeds its reference data this way).
type MigrateHook func(ctx context.Context, pool *pgxpool.Pool) error

// MigrateURL is the database URL the migrate command connects with: MIGRATE_DATABASE_URL (the schema owner role),
// else DATABASE_URL. The chart's migration Job sets both to the owner role's URL.
func MigrateURL(getenv func(string) string) (string, error) {
	if u := getenv("MIGRATE_DATABASE_URL"); u != "" {
		return u, nil
	}
	if u := getenv("DATABASE_URL"); u != "" {
		return u, nil
	}
	return "", errors.New("MIGRATE_DATABASE_URL or DATABASE_URL is required to migrate")
}

// RunMigrateCommand reports whether args (os.Args) ask for MigrateCommand. When they do, it connects with MigrateURL,
// applies migrations with Migrate (advisory-locked, so a Job retry or an overlapping dev pod with MIGRATE_ON_START cannot
// apply a file twice), runs the hooks and returns their outcome; nothing else of the service starts. Extra arguments
// after the command are refused, so a typo cannot fall through to serving.
func RunMigrateCommand(ctx context.Context, args []string, getenv func(string) string, migrations fs.FS, hooks ...MigrateHook) (bool, error) {
	if len(args) < 2 || args[1] != MigrateCommand {
		return false, nil
	}
	if len(args) > 2 {
		return true, fmt.Errorf("%s takes no arguments, got %q", MigrateCommand, args[2:])
	}
	url, err := MigrateURL(getenv)
	if err != nil {
		return true, err
	}
	pool, err := Connect(ctx, url)
	if err != nil {
		return true, fmt.Errorf("connect migration database: %w", err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool, migrations); err != nil {
		return true, fmt.Errorf("migrate: %w", err)
	}
	for _, hook := range hooks {
		if err := hook(ctx, pool); err != nil {
			return true, err
		}
	}
	return true, nil
}

// HandleMigrateCommand is the one line every service main calls first: when the process was started as
// `<binary> migrate` it applies the migrations (RunMigrateCommand with os.Args and os.Getenv), logs the outcome with the
// default slog logger and exits, 0 on success and 1 on failure. Otherwise it returns and the service starts as before.
func HandleMigrateCommand(migrations fs.FS, hooks ...MigrateHook) {
	ran, err := RunMigrateCommand(context.Background(), os.Args, os.Getenv, migrations, hooks...)
	if !ran {
		return
	}
	if err != nil {
		slog.Error("migrations failed", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")
	os.Exit(0)
}
