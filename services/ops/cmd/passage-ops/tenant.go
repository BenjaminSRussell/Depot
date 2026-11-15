package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/BenjaminSRussell/passage/services/ops/internal/provision"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

// runTenant is `passage-ops tenant provision|decommission`: it runs the steps of a tenant provision or decommission with the
// executors of this machine and reports each to the ops API. The API holds the plan and the residency and suspension checks; this
// command holds the credentials. --dry-run asks every adapter what it would do and reports nothing.
//
//	passage-ops tenant provision --tenant <id> [--dry-run]
//	passage-ops tenant decommission --tenant <id> --reason <text> [--dry-run]
func runTenant(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	if len(args) == 0 || (args[0] != "provision" && args[0] != "decommission") {
		return errors.New("usage: passage-ops tenant provision|decommission --tenant <id> [--reason <text>] [--dry-run]")
	}
	fs := flag.NewFlagSet("tenant "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var tenantID, reason string
	var dry bool
	fs.StringVar(&tenantID, "tenant", "", "tenant id")
	fs.StringVar(&reason, "reason", "", "why the tenant is decommissioned (kept in the audit trail)")
	fs.BoolVar(&dry, "dry-run", false, "ask each adapter what it would do; change and report nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if tenantID == "" {
		return errors.New("--tenant is required")
	}
	c, err := newClient(getenv, d.httpClient)
	if err != nil {
		return err
	}
	exec := provision.LogExecutors(slog.Default())
	if d.provisioners != nil {
		if exec, err = d.provisioners(getenv); err != nil {
			return err
		}
	}
	rep := apiReporter{c: c}
	runner := &provision.Runner{Exec: exec, Reporter: rep, DryRun: dry}
	var prov store.Provision
	if _, err := c.do(ctx, http.MethodGet, "/v1/ops/tenants/"+url.PathEscape(tenantID), nil, nil, &prov); err != nil {
		return err
	}
	t := provision.Tenant{ID: prov.TenantID, Name: prov.Name, Residency: prov.Residency, Region: prov.Region, Tier: prov.Tier}
	var res provision.Result
	if args[0] == "provision" {
		res, err = runner.Provision(ctx, t)
	} else {
		if !dry {
			if reason == "" {
				return errors.New("--reason is required")
			}
			// The API refuses unless identity reports the tenant suspended (OPS-1731), and audits the request; a repeat is a resume.
			var dec store.Decommission
			if _, err := c.do(ctx, http.MethodPost, "/v1/ops/tenants/"+url.PathEscape(tenantID)+":decommission", nil, map[string]string{"reason": reason}, &dec); err != nil {
				return err
			}
		}
		res, err = runner.Decommission(ctx, t)
	}
	for _, s := range res.Steps {
		_, _ = fmt.Fprintf(out, "%-10s %-8s %s\n", s.Action, s.Status, s.Detail)
	}
	return err
}

// apiReporter is the ops API as the provision runner sees it.
type apiReporter struct{ c *client }

func (r apiReporter) ProvisionDone(ctx context.Context, tenantID string) (map[string]bool, error) {
	var p store.Provision
	if _, err := r.c.do(ctx, http.MethodGet, "/v1/ops/tenants/"+url.PathEscape(tenantID), nil, nil, &p); err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, s := range p.Steps {
		if s.Status == "DONE" {
			done[s.Action] = true
		}
	}
	return done, nil
}

func (r apiReporter) ReportProvisionStep(ctx context.Context, tenantID, action, status, detail string) error {
	_, err := r.c.do(ctx, http.MethodPut, "/v1/ops/tenants/"+url.PathEscape(tenantID)+"/provision-steps/"+url.PathEscape(action), nil, map[string]string{"status": status, "detail": detail}, nil)
	return err
}

func (r apiReporter) DecommissionDone(ctx context.Context, tenantID string) (map[string]bool, error) {
	var out struct {
		Decommission store.Decommission `json:"decommission"`
	}
	if _, err := r.c.do(ctx, http.MethodGet, "/v1/ops/tenants/"+url.PathEscape(tenantID)+"/decommission", nil, nil, &out); err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, s := range out.Decommission.Steps {
		if s.Status == "DONE" {
			done[s.Action] = true
		}
	}
	return done, nil
}

func (r apiReporter) ReportDecommissionStep(ctx context.Context, tenantID, action, status, detail string) error {
	_, err := r.c.do(ctx, http.MethodPut, "/v1/ops/tenants/"+url.PathEscape(tenantID)+"/decommission-steps/"+url.PathEscape(action), nil, map[string]string{"status": status, "detail": detail}, nil)
	return err
}
