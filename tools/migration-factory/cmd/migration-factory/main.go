// Command migration-factory serves the migration factory API: open-items pack runs with
// control totals, the legacy cross-reference and the cutover runbook (DPT-49).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"

	"github.com/BenjaminSRussell/depot/tools/migration-factory/internal/api"
	"github.com/BenjaminSRussell/depot/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/depot/tools/migration-factory/internal/target"
	"github.com/BenjaminSRussell/depot/tools/migration-factory/migrations"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	// "<binary> migrate" applies the migrations as the owner role and exits: the chart's PreSync migration Job (D28).
	dbx.HandleMigrateCommand(migrations.FS)
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("migration-factory stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger(w io.Writer) *slog.Logger {
	renamed := map[string]string{slog.TimeKey: "timestamp", slog.MessageKey: "message"}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if name, ok := renamed[a.Key]; ok {
			a.Key = name
		}
		return a
	}})
	return slog.New(handler).With("service", "migration-factory")
}

// targetConfig reads the base address in envURL and the service signing key the factory's calls are signed with. The
// key must belong to the service named "migration-factory" (DEPOT_SERVICE_NAME), the caller the service grants are
// written for. With neither set the service starts and the load step answers 503; an address without a key, or a key
// for another service, stops it, because every call would be refused.
func targetConfig(getenv func(string) string, envURL string) (target.Config, error) {
	base := getenv(envURL)
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return target.Config{}, err
	}
	if base != "" {
		if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return target.Config{}, fmt.Errorf("%s must be an http or https base address such as http://receivables:8080", envURL)
		}
	}
	switch {
	case base != "" && signer == nil:
		return target.Config{}, fmt.Errorf("%s is set but no service signing key is (DEPOT_SERVICE_SIGNING_KEY or DEPOT_SERVICE_SIGNING_KEY_FILE)", envURL)
	case signer != nil && getenv(tenant.EnvServiceName) != target.ServiceName:
		return target.Config{}, fmt.Errorf("%s must be %q for the factory's service grants", tenant.EnvServiceName, target.ServiceName)
	case base == "" || signer == nil:
		return target.Config{}, nil
	}
	return target.Config{BaseURL: base, Signer: signer}, nil
}

// receivablesTarget reads RECEIVABLES_URL (see targetConfig); without it the load step answers 503 MIG-1520.
func receivablesTarget(getenv func(string) string) (*target.Receivables, error) {
	cfg, err := targetConfig(getenv, "RECEIVABLES_URL")
	if err != nil {
		return nil, err
	}
	return target.NewReceivables(cfg), nil
}

// payablesTarget reads PAYABLES_URL (see targetConfig); without it the AP load answers 503 MIG-1550.
func payablesTarget(getenv func(string) string) (*target.Payables, error) {
	cfg, err := targetConfig(getenv, "PAYABLES_URL")
	if err != nil {
		return nil, err
	}
	return target.NewPayables(cfg), nil
}

// inventoryTarget reads INVENTORY_URL (see targetConfig); without it the onhand load answers 503 MIG-1560.
func inventoryTarget(getenv func(string) string) (*target.Inventory, error) {
	cfg, err := targetConfig(getenv, "INVENTORY_URL")
	if err != nil {
		return nil, err
	}
	return target.NewInventory(cfg), nil
}

// treasuryTarget reads TREASURY_URL (see targetConfig); without it the bank_balances load answers 503 MIG-1560.
func treasuryTarget(getenv func(string) string) (*target.Treasury, error) {
	cfg, err := targetConfig(getenv, "TREASURY_URL")
	if err != nil {
		return nil, err
	}
	return target.NewTreasury(cfg), nil
}

// salesTarget reads SALES_URL (see targetConfig); without it the SALES lines of an open_orders load answer 503 MIG-1570.
func salesTarget(getenv func(string) string) (*target.Orders, error) {
	cfg, err := targetConfig(getenv, "SALES_URL")
	if err != nil {
		return nil, err
	}
	return target.NewSales(cfg), nil
}

// purchasingTarget reads PURCHASING_URL (see targetConfig); without it the PURCHASE lines of an open_orders load answer 503 MIG-1570.
func purchasingTarget(getenv func(string) string) (*target.Orders, error) {
	cfg, err := targetConfig(getenv, "PURCHASING_URL")
	if err != nil {
		return nil, err
	}
	return target.NewPurchasing(cfg), nil
}

// assetsTarget reads ASSETS_URL (see targetConfig); without it a fixed_assets load answers 503 MIG-1580.
func assetsTarget(getenv func(string) string) (*target.Staged, error) {
	cfg, err := targetConfig(getenv, "ASSETS_URL")
	if err != nil {
		return nil, err
	}
	return target.NewAssets(cfg), nil
}

// productionTarget reads PRODUCTION_URL (see targetConfig); without it a wip_prod load answers 503 MIG-1580.
func productionTarget(getenv func(string) string) (*target.Staged, error) {
	cfg, err := targetConfig(getenv, "PRODUCTION_URL")
	if err != nil {
		return nil, err
	}
	return target.NewProduction(cfg), nil
}

// payrollTarget reads PAYROLL_URL (see targetConfig); without it a payroll_ytd load answers 503 MIG-1580.
func payrollTarget(getenv func(string) string) (*target.Staged, error) {
	cfg, err := targetConfig(getenv, "PAYROLL_URL")
	if err != nil {
		return nil, err
	}
	return target.NewPayroll(cfg), nil
}

// xrefTarget reads MIGRATION_URL (see targetConfig): the migration service whose cross-reference resolves the legacy customers and
// vendors of an open-items run (H-S8-6, #49) and the items, customers and vendors of an open-orders load. Without it a customer must be on the record or in load.customerIds.
func xrefTarget(getenv func(string) string) (*target.Xref, error) {
	cfg, err := targetConfig(getenv, "MIGRATION_URL")
	if err != nil {
		return nil, err
	}
	return target.NewXref(cfg), nil
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	receivables, err := receivablesTarget(getenv)
	if err != nil {
		return err
	}
	payables, err := payablesTarget(getenv)
	if err != nil {
		return err
	}
	inventory, err := inventoryTarget(getenv)
	if err != nil {
		return err
	}
	treasury, err := treasuryTarget(getenv)
	if err != nil {
		return err
	}
	sales, err := salesTarget(getenv)
	if err != nil {
		return err
	}
	purchasing, err := purchasingTarget(getenv)
	if err != nil {
		return err
	}
	assets, err := assetsTarget(getenv)
	if err != nil {
		return err
	}
	production, err := productionTarget(getenv)
	if err != nil {
		return err
	}
	payroll, err := payrollTarget(getenv)
	if err != nil {
		return err
	}
	xref, err := xrefTarget(getenv)
	if err != nil {
		return err
	}
	// LEDGER_URL is optional: without it the parallel-run replay and the rollback compensations
	// answer 503 MIG-1530; with it the service signing key is required.
	ledger, err := ledgerclient.FromEnv(getenv, target.ServiceName)
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := api.New(store.New(pool)).WithTarget(receivables).WithPayables(payables).WithInventory(inventory).WithTreasury(treasury).WithSales(sales).WithPurchasing(purchasing).WithAssets(assets).WithProduction(production).WithPayroll(payroll).WithXref(xref)
	if ledger != nil {
		srv = srv.WithLedger(ledger)
	}
	return httpx.Serve(ctx, addr, srv.Routes())
}
