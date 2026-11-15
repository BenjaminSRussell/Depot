// Package provision runs the steps of a tenant provision and of a tenant decommission (PSG-50, FR-50.8;
// docs/ops/tenant-provisioning.md). The systems the steps change sit behind four small interfaces (a Keycloak realm, the
// databases and RLS schemas, Kafka ACLs and topics, the lakehouse namespace) so the runner is tested with fakes and run with
// DryRun, which asks each adapter what it would do and changes nothing. Every adapter call is idempotent: a step that already
// holds is a success, so a rerun resumes at the first step the API does not have as DONE.
//
// The adapters in this package are LogAdapter only: it records what would change, changes nothing, and fails a real run (ErrNotConfigured), because the repository has
// no Keycloak admin, database owner, Kafka admin or lakehouse client the ops service can use (identity holds the Keycloak admin
// client in its own module). A real adapter implements the same four interfaces and is passed in where Executors is built.
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Tenant is what the adapters need to know about the tenant.
type Tenant struct {
	ID        string
	Name      string
	Residency string
	Region    string
	Tier      string
}

// RealmAdmin creates and removes the tenant's Keycloak realm.
type RealmAdmin interface {
	EnsureRealm(ctx context.Context, t Tenant, dryRun bool) (string, error)
	DeleteRealm(ctx context.Context, t Tenant, dryRun bool) (string, error)
}

// DatabaseAdmin creates and drops the tenant's databases or schemas with their RLS grants.
type DatabaseAdmin interface {
	EnsureDatabases(ctx context.Context, t Tenant, dryRun bool) (string, error)
	DropDatabases(ctx context.Context, t Tenant, dryRun bool) (string, error)
}

// KafkaAdmin creates and removes the tenant's Kafka ACLs and topics.
type KafkaAdmin interface {
	EnsureAccess(ctx context.Context, t Tenant, dryRun bool) (string, error)
	RemoveAccess(ctx context.Context, t Tenant, dryRun bool) (string, error)
}

// LakehouseAdmin creates and removes the tenant's lakehouse namespace (object-store prefixes and catalog namespace).
type LakehouseAdmin interface {
	EnsureNamespace(ctx context.Context, t Tenant, dryRun bool) (string, error)
	DropNamespace(ctx context.Context, t Tenant, dryRun bool) (string, error)
}

// Executors is the four systems.
type Executors struct {
	Realm     RealmAdmin
	Databases DatabaseAdmin
	Kafka     KafkaAdmin
	Lakehouse LakehouseAdmin
}

// Reporter is the ops API as the runner sees it.
type Reporter interface {
	// Provision returns the steps of the tenant's provision that are DONE (action names).
	ProvisionDone(ctx context.Context, tenantID string) (map[string]bool, error)
	ReportProvisionStep(ctx context.Context, tenantID, action, status, detail string) error
	// DecommissionDone returns the steps of the tenant's decommission that are DONE.
	DecommissionDone(ctx context.Context, tenantID string) (map[string]bool, error)
	ReportDecommissionStep(ctx context.Context, tenantID, action, status, detail string) error
}

// ErrLicensePending is returned by Provision when every executor step is DONE but the tenant's licence is not installed yet
// (the API refuses the license step with OPS-1724): install the licence, then run the provision again.
var ErrLicensePending = errors.New("provisioning is waiting for the tenant's licence: install it (PUT /v1/ops/tenants/{id}/license), then run the provision again")

// ProvisionOrder is the order steps run in; the license step is reported last, after the executors.
var ProvisionOrder = []string{"realm", "databases", "kafka-acls", "lakehouse"}

// DecommissionOrder is ProvisionOrder reversed: the namespace and the access go before the databases, and the realm last, so a
// failed run never leaves a database whose users can no longer be resolved.
var DecommissionOrder = []string{"lakehouse", "kafka-acls", "databases", "realm"}

// Runner executes provisions and decommissions.
type Runner struct {
	Exec     Executors
	Reporter Reporter
	// DryRun asks the adapters what they would do and reports nothing to the API.
	DryRun bool
	Log    *slog.Logger
}

// Result is what a run did, step by step.
type Result struct {
	Steps []StepResult
}

// StepResult is one step of a run.
type StepResult struct {
	Action string
	Status string // DONE, SKIPPED (already DONE in the API), PLANNED (dry run) or FAILED
	Detail string
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (e Executors) validate() error {
	if e.Realm == nil || e.Databases == nil || e.Kafka == nil || e.Lakehouse == nil {
		return errors.New("provision: every executor (realm, databases, kafka, lakehouse) is required")
	}
	return nil
}

func (e Executors) apply(ctx context.Context, action string, t Tenant, dry bool) (string, error) {
	switch action {
	case "realm":
		return e.Realm.EnsureRealm(ctx, t, dry)
	case "databases":
		return e.Databases.EnsureDatabases(ctx, t, dry)
	case "kafka-acls":
		return e.Kafka.EnsureAccess(ctx, t, dry)
	case "lakehouse":
		return e.Lakehouse.EnsureNamespace(ctx, t, dry)
	}
	return "", fmt.Errorf("provision: unknown step %q", action)
}

func (e Executors) remove(ctx context.Context, action string, t Tenant, dry bool) (string, error) {
	switch action {
	case "realm":
		return e.Realm.DeleteRealm(ctx, t, dry)
	case "databases":
		return e.Databases.DropDatabases(ctx, t, dry)
	case "kafka-acls":
		return e.Kafka.RemoveAccess(ctx, t, dry)
	case "lakehouse":
		return e.Lakehouse.DropNamespace(ctx, t, dry)
	}
	return "", fmt.Errorf("provision: unknown step %q", action)
}

// Provision runs the executor steps in order, skipping the ones the API already has as DONE, and reports each. A failed step is
// reported FAILED and stops the run (a rerun retries it). It ends by reporting the license step, which the API accepts once the
// tenant's licence is installed; until then it returns ErrLicensePending.
func (r *Runner) Provision(ctx context.Context, t Tenant) (Result, error) {
	return r.run(ctx, t, ProvisionOrder, r.Exec.apply, r.Reporter.ProvisionDone, r.Reporter.ReportProvisionStep, true)
}

// Decommission removes the tenant's systems in reverse order. It does not decide whether that is allowed: the API refuses to
// open a decommission for a tenant that is not suspended, and the caller opens it first (`passage-ops decommission` does).
func (r *Runner) Decommission(ctx context.Context, t Tenant) (Result, error) {
	return r.run(ctx, t, DecommissionOrder, r.Exec.remove, r.Reporter.DecommissionDone, r.Reporter.ReportDecommissionStep, false)
}

type stepFn func(ctx context.Context, action string, t Tenant, dry bool) (string, error)

func (r *Runner) run(ctx context.Context, t Tenant, order []string, do stepFn, done func(context.Context, string) (map[string]bool, error),
	report func(context.Context, string, string, string, string) error, licence bool) (Result, error) {
	var res Result
	if err := r.Exec.validate(); err != nil {
		return res, err
	}
	have := map[string]bool{}
	if !r.DryRun {
		var err error
		if have, err = done(ctx, t.ID); err != nil {
			return res, err
		}
	}
	for _, action := range order {
		if have[action] {
			res.Steps = append(res.Steps, StepResult{Action: action, Status: "SKIPPED", Detail: "already DONE"})
			continue
		}
		detail, err := do(ctx, action, t, r.DryRun)
		switch {
		case err != nil:
			res.Steps = append(res.Steps, StepResult{Action: action, Status: "FAILED", Detail: err.Error()})
			if !r.DryRun {
				if rerr := report(ctx, t.ID, action, "FAILED", truncate(err.Error())); rerr != nil {
					return res, errors.Join(err, rerr)
				}
			}
			return res, fmt.Errorf("provision: step %s: %w", action, err)
		case r.DryRun:
			res.Steps = append(res.Steps, StepResult{Action: action, Status: "PLANNED", Detail: detail})
		default:
			if err := report(ctx, t.ID, action, "DONE", truncate(detail)); err != nil {
				return res, err
			}
			res.Steps = append(res.Steps, StepResult{Action: action, Status: "DONE", Detail: detail})
		}
	}
	if licence && !r.DryRun && !have["license"] {
		if err := r.Reporter.ReportProvisionStep(ctx, t.ID, "license", "DONE", "the tenant's licence is installed"); err != nil {
			if strings.Contains(err.Error(), "OPS-1724") {
				return res, ErrLicensePending
			}
			return res, err
		}
		res.Steps = append(res.Steps, StepResult{Action: "license", Status: "DONE", Detail: "the tenant's licence is installed"})
	}
	return res, nil
}

func truncate(s string) string {
	if len(s) > 1000 {
		return s[:1000]
	}
	return s
}

// LogAdapter implements all four interfaces by logging what would change and changing nothing. It is the adapter until a real one
// for the system exists; its detail says so, so a step it "completes" is visible in the API as not performed.
type LogAdapter struct{ Log *slog.Logger }

// ErrNotConfigured is what the LogAdapter answers to a real (not dry) run: it performed nothing, so no step may be reported DONE.
var ErrNotConfigured = errors.New("no adapter is configured for this system: nothing was changed (dry runs still work)")

func (a LogAdapter) note(system, verb string, t Tenant, dry bool) (string, error) {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	msg := fmt.Sprintf("logged only, no %s adapter is configured: would %s for tenant %s in region %s", system, verb, t.ID, t.Region)
	log.Info("provision step", "system", system, "verb", verb, "tenant", t.ID, "dryRun", dry)
	if !dry {
		// A step that changed nothing must never become DONE: the tenant would reach PROVISIONED or DECOMMISSIONED, with its event
		// and audit entry, without anything having happened.
		return "", fmt.Errorf("%s: %w (would %s)", system, ErrNotConfigured, verb)
	}
	return msg, nil
}

func (a LogAdapter) EnsureRealm(_ context.Context, t Tenant, dry bool) (string, error) {
	return a.note("Keycloak", "create realm passage-"+t.ID, t, dry)
}
func (a LogAdapter) DeleteRealm(_ context.Context, t Tenant, dry bool) (string, error) {
	return a.note("Keycloak", "delete realm passage-"+t.ID, t, dry)
}
func (a LogAdapter) EnsureDatabases(_ context.Context, t Tenant, dry bool) (string, error) {
	return a.note("database", "create the tenant's schemas and RLS grants", t, dry)
}
func (a LogAdapter) DropDatabases(_ context.Context, t Tenant, dry bool) (string, error) {
	return a.note("database", "drop the tenant's schemas", t, dry)
}
func (a LogAdapter) EnsureAccess(_ context.Context, t Tenant, dry bool) (string, error) {
	return a.note("Kafka", "create the tenant's ACLs and topics", t, dry)
}
func (a LogAdapter) RemoveAccess(_ context.Context, t Tenant, dry bool) (string, error) {
	return a.note("Kafka", "remove the tenant's ACLs and topics", t, dry)
}
func (a LogAdapter) EnsureNamespace(_ context.Context, t Tenant, dry bool) (string, error) {
	return a.note("lakehouse", "create the tenant's namespace", t, dry)
}
func (a LogAdapter) DropNamespace(_ context.Context, t Tenant, dry bool) (string, error) {
	return a.note("lakehouse", "drop the tenant's namespace", t, dry)
}

// LogExecutors is every system on the LogAdapter.
func LogExecutors(log *slog.Logger) Executors {
	a := LogAdapter{Log: log}
	return Executors{Realm: a, Databases: a, Kafka: a, Lakehouse: a}
}
