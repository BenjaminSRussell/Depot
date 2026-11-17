package dbx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateCommand is the first argument that runs a service binary as its own schema migration step instead of the
// service: `<binary> migrate`. Every service chart with migrations runs it from the owner-role migration Job
// (templates/migrate-job.yaml, an Argo CD PreSync hook; ADR-0012 rule 3, D28), so serving pods keep MIGRATE_ON_START=false
// and connect as depot_app only.
const MigrateCommand = "migrate"

// AppRole is the runtime role (NOBYPASSRLS, not an owner). The migrate command refuses to run as it, or as a role that is a
// member of it, unless MIGRATE_ALLOW_APP_ROLE=true: tables it creates would belong to the serving role.
const AppRole = "depot_app"

// DefaultMigrateRetry bounds how long the migrate command waits for the database (MIGRATE_RETRY_TIMEOUT overrides it), well
// under the Job's activeDeadlineSeconds (600 s by default).
const DefaultMigrateRetry = 5 * time.Minute

// The back-off between two connection attempts: migrateRetryFirst, doubled each time up to migrateRetryMax.
var (
	migrateRetryFirst = time.Second
	migrateRetryMax   = 30 * time.Second
)

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

// MigrateRetry is how long the migrate command keeps retrying a database it cannot reach: MIGRATE_RETRY_TIMEOUT (a Go
// duration; 0 tries once), else DefaultMigrateRetry.
func MigrateRetry(getenv func(string) string) (time.Duration, error) {
	raw := getenv("MIGRATE_RETRY_TIMEOUT")
	if raw == "" {
		return DefaultMigrateRetry, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("MIGRATE_RETRY_TIMEOUT must be a non-negative Go duration, got %q", raw)
	}
	return d, nil
}

// RunMigrateCommand reports whether args (os.Args) ask for MigrateCommand. When they do, it connects with MigrateURL,
// refuses the app role (see AppRole), applies migrations with Migrate (advisory-locked, so a Job retry or an overlapping
// dev pod with MIGRATE_ON_START cannot apply a file twice), runs the hooks and returns their outcome; nothing else of the
// service starts. Extra arguments after the command are refused, so a typo cannot fall through to serving.
//
// A database that cannot be reached (refused or reset connection, DNS failure, timeout, a server starting up or shutting
// down, too many connections) is retried with exponential back-off for MigrateRetry, each attempt logged; a migration's
// own SQL error, a hook error, a wrong password or a missing database fails at once. A file interrupted by a lost
// connection was rolled back with its schema_migrations row, so the retry applies it again.
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
	budget, err := MigrateRetry(getenv)
	if err != nil {
		return true, err
	}
	allowApp := getenv("MIGRATE_ALLOW_APP_ROLE") == "true"
	pool, err := Connect(ctx, url)
	if err != nil {
		return true, fmt.Errorf("connect migration database: %w", err)
	}
	defer pool.Close()
	deadline := time.Now().Add(budget)
	wait := migrateRetryFirst
	for attempt := 1; ; attempt++ {
		err = migrateOnce(ctx, pool, migrations, allowApp)
		if err == nil || !IsConnectionError(err) || time.Now().Add(wait).After(deadline) {
			break
		}
		slog.Warn("migration database not reachable; retrying", "attempt", attempt, "retryIn", wait.String(), "err", err)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return true, ctx.Err()
		case <-timer.C:
		}
		wait = min(2*wait, migrateRetryMax)
	}
	if err != nil {
		return true, err
	}
	for _, hook := range hooks {
		if err := hook(ctx, pool); err != nil {
			return true, err
		}
	}
	return true, nil
}

// migratePing bounds one connection attempt (a black-holed address would otherwise wait for the OS TCP timeout).
var migratePing = 10 * time.Second

func migrateOnce(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS, allowApp bool) error {
	pingCtx, cancel := context.WithTimeout(ctx, migratePing)
	err := pool.Ping(pingCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("reach migration database: %w", err)
	}
	if !allowApp {
		if err := refuseAppRole(ctx, pool); err != nil {
			return err
		}
	}
	if err := Migrate(ctx, pool, migrations); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// appRoleQuery is true when the session's role is depot_app or a member of it (a superuser is a member of every role by
// pg_has_role, so it is left out).
const appRoleQuery = `select r.rolname = '` + AppRole + `' or (not r.rolsuper
  and exists (select 1 from pg_roles where rolname = '` + AppRole + `')
  and pg_has_role(r.oid, '` + AppRole + `', 'MEMBER'))
from pg_roles r where r.rolname = current_user`

func refuseAppRole(ctx context.Context, pool *pgxpool.Pool) error {
	var app bool
	var user string
	if err := pool.QueryRow(ctx, appRoleQuery).Scan(&app); err != nil {
		return err
	}
	if !app {
		return nil
	}
	_ = pool.QueryRow(ctx, `select current_user`).Scan(&user)
	return fmt.Errorf("refusing to migrate as %q, the serving role %s or a member of it: the tables would belong to it. Use the owner role's URL (MIGRATE_DATABASE_URL), or set MIGRATE_ALLOW_APP_ROLE=true where %s really owns the schema (dev only)", user, AppRole, AppRole)
}

// IsConnectionError reports whether err means the database could not be reached or dropped the session, as opposed to a
// statement's own error: a failed connection attempt (except a rejected login or a missing database, which no retry
// fixes), a network error or timeout, or a server error of class 08 (connection exception), 57P01-57P03 (shutdown, cannot
// connect now) or 53300 (too many connections).
func IsConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var pgErr *pgconn.PgError
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		if errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "28") || pgErr.Code == "3D000") {
			return false
		}
		return true
	}
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"), pgErr.Code == "57P01", pgErr.Code == "57P02", pgErr.Code == "57P03", pgErr.Code == "53300":
			return true
		}
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) || pgconn.Timeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(err.Error(), "unexpected EOF") || strings.Contains(err.Error(), "connection reset")
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
