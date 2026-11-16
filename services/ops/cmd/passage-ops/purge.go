package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/BenjaminSRussell/passage/services/ops/internal/provision"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

// runVerifyExport is `passage-ops tenant verify-export --tenant <id> --export-id <id> [--note <text>]`: it records, in the ops API, that
// the decommissioned tenant's export was verified (by the caller, now). The purge needs it.
func runVerifyExport(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	fs := flag.NewFlagSet("tenant verify-export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var tenantID, exportID, note string
	fs.StringVar(&tenantID, "tenant", "", "tenant id")
	fs.StringVar(&exportID, "export-id", "", "identifier of the verified export")
	fs.StringVar(&note, "note", "", "what was verified")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tenantID == "" || exportID == "" {
		return errors.New("--tenant and --export-id are required")
	}
	c, err := newClient(getenv, d.httpClient)
	if err != nil {
		return err
	}
	var p store.Purge
	if _, err := c.do(ctx, http.MethodPut, "/v1/ops/tenants/"+url.PathEscape(tenantID)+"/export-verification", nil, map[string]string{"exportId": exportID, "note": note}, &p); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "export %s recorded as verified by %s at %s\n", p.ExportID, p.ExportVerifiedBy, p.ExportVerifiedAt.Format("2006-01-02 15:04:05Z07:00"))
	return nil
}

// runPurge is `passage-ops tenant purge --tenant <id> [--dry-run]`. The API authorises it (decommission DONE, export verified) and
// audits the request; the deletion uses the purge credentials of this machine (OPS_PURGE_DATABASES), separate from the provisioning
// ones; the result, retained tables included, is reported back and audited.
func runPurge(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	fs := flag.NewFlagSet("tenant purge", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var tenantID string
	var dry bool
	fs.StringVar(&tenantID, "tenant", "", "tenant id")
	fs.BoolVar(&dry, "dry-run", false, "say what would be deleted; change and report nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tenantID == "" {
		return errors.New("--tenant is required")
	}
	purger, err := provision.PurgerFromEnv(getenv)
	if err != nil {
		if dry && errors.Is(err, provision.ErrNotConfigured) {
			_, _ = fmt.Fprintln(out, "purge credentials are not configured (OPS_PURGE_DATABASES): a real run would be refused")
			return nil
		}
		return err
	}
	if dry {
		plan, err := purger.Plan(tenantID)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(out, plan)
		return nil
	}
	c, err := newClient(getenv, d.httpClient)
	if err != nil {
		return err
	}
	if _, err := c.do(ctx, http.MethodPost, "/v1/ops/tenants/"+url.PathEscape(tenantID)+":purge", nil, nil, nil); err != nil {
		return err
	}
	rep, perr := purger.Purge(ctx, tenantID)
	status, detail := "DONE", rep.Detail()
	if perr != nil {
		status, detail = "FAILED", perr.Error()
	}
	if len(detail) > 4000 {
		detail = detail[:4000]
	}
	if _, err := c.do(ctx, http.MethodPut, "/v1/ops/tenants/"+url.PathEscape(tenantID)+"/purge-result", nil, map[string]string{"status": status, "detail": detail}, nil); err != nil {
		return errors.Join(perr, err)
	}
	_, _ = fmt.Fprintf(out, "purge %s: %s\n", status, detail)
	return perr
}
