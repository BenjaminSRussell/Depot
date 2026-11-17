// Command depot-ops is the operator's command line for the ops control plane (DPT-50, FR-50.5):
//
//	depot-ops failover --to <region> [--accept-data-loss] [--dry-run] [--event <id>]
//	depot-ops failover approve --request <id>
//	depot-ops failover resume --event <id>
//	depot-ops break-glass open --reason <code> --ticket <ref> [--minutes N]
//	depot-ops break-glass list|revoke|review ...
//	depot-ops lag-report --region <name> [--once]
//	depot-ops tenant provision|decommission --tenant <id> [--reason <text>] [--dry-run]
//
// A failover first asks the ops API to record it (fence and promote in the region table, OPS-1701/1702), then runs the
// ordered infrastructure steps with the credentials of the machine it runs on and reports each step back. A rerun with the
// same --event, or `resume`, continues at the first step that is not DONE.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Getenv, defaultDeps()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "depot-ops:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	if len(args) == 0 {
		return errors.New("usage: depot-ops failover|break-glass|lag-report|tenant ... (see --help of each)")
	}
	switch args[0] {
	case "failover":
		return runFailover(ctx, args[1:], out, getenv, d)
	case "break-glass":
		return runBreakGlass(ctx, args[1:], out, getenv, d)
	case "lag-report":
		return runLagReport(ctx, args[1:], out, getenv, d)
	case "tenant":
		return runTenant(ctx, args[1:], out, getenv, d)
	}
	return fmt.Errorf("unknown command %q", args[0])
}
