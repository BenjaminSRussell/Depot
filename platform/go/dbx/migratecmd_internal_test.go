package dbx

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsConnectionErrorRetriesOnlyAnUnreachableDatabase(t *testing.T) {
	_, refused := pgconn.Connect(context.Background(), "postgres://owner@127.0.0.1:1/x?sslmode=disable&connect_timeout=2")
	var connectErr *pgconn.ConnectError
	if !errors.As(refused, &connectErr) {
		t.Fatalf("want a *pgconn.ConnectError, got %T %v", refused, refused)
	}
	retry := []error{
		refused, // connection refused
		fmt.Errorf("migrate: %w", &pgconn.PgError{Code: "57P03"}), // the database system is starting up
		&pgconn.PgError{Code: "57P01"},                            // admin shutdown
		&pgconn.PgError{Code: "08006"},
		&pgconn.PgError{Code: "53300"},
		&net.OpError{Op: "dial", Err: errors.New("connection refused")},
		fmt.Errorf("reach migration database: %w", context.DeadlineExceeded),
	}
	for _, err := range retry {
		if !IsConnectionError(err) {
			t.Errorf("%v: want a retry", err)
		}
	}
	noRetry := []error{
		nil,
		context.Canceled,
		fmt.Errorf("migrate: migration 0002_x.sql: %w", &pgconn.PgError{Code: "42601"}), // syntax error
		&pgconn.PgError{Code: "23505"},
		&pgconn.PgError{Code: "42501"}, // insufficient privilege
		errors.New(`refusing to migrate as "depot_app"`),
		fmt.Errorf("connect: %w", x509.UnknownAuthorityError{}), // TLS: unknown CA
		fmt.Errorf("tls: %w", x509.HostnameError{Host: "db"}),   // TLS: wrong name
		fmt.Errorf("connect: %w", &tls.CertificateVerificationError{Err: errors.New("expired")}),
		errors.New("failed to connect: server refused TLS connection"),
	}
	for _, err := range noRetry {
		if IsConnectionError(err) {
			t.Errorf("%v: want no retry", err)
		}
	}
}

func TestMigrateRetryIsConfigurable(t *testing.T) {
	get := func(v string) func(string) string { return func(string) string { return v } }
	if d, err := MigrateRetry(get("")); err != nil || d != DefaultMigrateRetry {
		t.Fatalf("default: %v %v", d, err)
	}
	if d, err := MigrateRetry(get("90s")); err != nil || d != 90*time.Second {
		t.Fatalf("90s: %v %v", d, err)
	}
	for _, bad := range []string{"soon", "-1s"} {
		if _, err := MigrateRetry(get(bad)); err == nil {
			t.Fatalf("%q: want an error", bad)
		}
	}
}

// An unreachable database is retried with back-off, each attempt logged, until MIGRATE_RETRY_TIMEOUT runs out.
func TestRunMigrateCommandRetriesAnUnreachableDatabase(t *testing.T) {
	first, max, ping := migrateRetryFirst, migrateRetryMax, migratePing
	migrateRetryFirst, migrateRetryMax, migratePing = 50*time.Millisecond, 200*time.Millisecond, time.Second
	t.Cleanup(func() { migrateRetryFirst, migrateRetryMax, migratePing = first, max, ping })
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	env := map[string]string{"MIGRATE_DATABASE_URL": "postgres://owner@127.0.0.1:1/x?sslmode=disable", "MIGRATE_RETRY_TIMEOUT": "1s"}
	start := time.Now()
	ran, err := RunMigrateCommand(context.Background(), []string{"svc", "migrate"}, func(k string) string { return env[k] }, fstest.MapFS{})
	elapsed := time.Since(start)
	if !ran || err == nil || !IsConnectionError(err) {
		t.Fatalf("ran=%v err=%v, want a connection error after the retries", ran, err)
	}
	if attempts := strings.Count(logs.String(), "retrying"); attempts < 3 {
		t.Fatalf("%d retries logged, want several:\n%s", attempts, logs.String())
	}
	if elapsed < 700*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("gave up after %v, want about MIGRATE_RETRY_TIMEOUT (1s)", elapsed)
	}

	env["MIGRATE_RETRY_TIMEOUT"] = "0"
	logs.Reset()
	if _, err := RunMigrateCommand(context.Background(), []string{"svc", "migrate"}, func(k string) string { return env[k] }, fstest.MapFS{}); err == nil || strings.Contains(logs.String(), "retrying") {
		t.Fatalf("MIGRATE_RETRY_TIMEOUT=0: err=%v logs=%s, want one attempt", err, logs.String())
	}
}
