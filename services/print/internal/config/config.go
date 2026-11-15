// Package config reads the print service's operator settings from the environment.
// Invalid values stop the process at startup rather than falling back silently.
package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// DefaultRetries is the PRINT_RETRY default: one retry, so each device gets
// domain.FailoverAttempts (2) tries before the queue fails over (FR-32.13, AC-32.10).
const DefaultRetries = domain.FailoverAttempts - 1

// Config holds the tunable print settings (§6.32.10 and §6.32.13).
type Config struct {
	// Retries is PRINT_RETRY (print_retry_count): retries on one printer after its first
	// attempt, before the job fails over to the next printer of the queue.
	Retries int
	// RetentionDays is PRINT_JOB_RETENTION_DAYS: age after which finished jobs are purged.
	RetentionDays int
	// PurgeTenantIDs is PRINT_PURGE_TENANT_IDS: the tenants the daily job-log purge runs
	// for, as comma-separated UUIDs. Empty disables the schedule; POST /jobs:purge still works.
	PurgeTenantIDs []string
	// SMTPAddr (PRINT_SMTP_ADDR, host:port) turns on e-mail delivery through that relay; empty keeps the log adapter, which sends
	// nothing. SMTPFrom is the sender address and is required with it; the user name and password are optional.
	SMTPAddr, SMTPFrom, SMTPUsername, SMTPPassword string
	// ReceivablesURL (PRINT_RECEIVABLES_URL) and PartyURL (PRINT_PARTY_URL) are where the scheduled statements read open items and
	// customer names. Both or neither.
	ReceivablesURL, PartyURL string
}

// AttemptsPerDevice is the number of sends a device gets before failover.
func (c Config) AttemptsPerDevice() int { return c.Retries + 1 }

// Load validates the environment and returns the settings. getenv is os.Getenv in production.
func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	var err error
	if cfg.Retries, err = intSetting(getenv, "PRINT_RETRY", DefaultRetries, 0, domain.MaxRetriesPerDevice); err != nil {
		return Config{}, err
	}
	if cfg.RetentionDays, err = intSetting(getenv, "PRINT_JOB_RETENTION_DAYS", domain.DefaultRetentionDays, 1, domain.MaxRetentionDays); err != nil {
		return Config{}, err
	}
	if cfg.PurgeTenantIDs, err = tenantList(getenv, "PRINT_PURGE_TENANT_IDS"); err != nil {
		return Config{}, err
	}
	cfg.SMTPAddr, cfg.SMTPFrom = strings.TrimSpace(getenv("PRINT_SMTP_ADDR")), strings.TrimSpace(getenv("PRINT_SMTP_FROM"))
	cfg.SMTPUsername, cfg.SMTPPassword = getenv("PRINT_SMTP_USERNAME"), getenv("PRINT_SMTP_PASSWORD")
	if cfg.SMTPAddr != "" {
		if i := strings.LastIndexByte(cfg.SMTPAddr, ':'); i < 1 || i == len(cfg.SMTPAddr)-1 {
			return Config{}, fmt.Errorf("PRINT_SMTP_ADDR must be host:port")
		}
		if cfg.SMTPFrom == "" {
			return Config{}, fmt.Errorf("PRINT_SMTP_FROM is required with PRINT_SMTP_ADDR")
		}
	}
	cfg.ReceivablesURL, cfg.PartyURL = strings.TrimSpace(getenv("PRINT_RECEIVABLES_URL")), strings.TrimSpace(getenv("PRINT_PARTY_URL"))
	if (cfg.ReceivablesURL == "") != (cfg.PartyURL == "") {
		return Config{}, fmt.Errorf("PRINT_RECEIVABLES_URL and PRINT_PARTY_URL are set together or not at all")
	}
	return cfg, nil
}

// tenantList parses a comma-separated list of tenant UUIDs. A malformed or repeated id
// stops startup rather than silently skipping a tenant's purge.
func tenantList(getenv func(string) string, key string) ([]string, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		id := strings.ToLower(strings.TrimSpace(part))
		if !ids.Valid(id) || seen[id] {
			return nil, fmt.Errorf("%s must be a comma-separated list of distinct tenant UUIDs", key)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func intSetting(getenv func(string) string, key string, fallback, lo, hi int) (int, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, lo, hi)
	}
	return n, nil
}
