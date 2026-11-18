package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"

	"github.com/BenjaminSRussell/depot/services/search/internal/reconcile"
)

// Exit statuses of `search reconcile`.
const (
	reconcileMatch    = 0
	reconcileDiffers  = 1
	reconcileFailed   = 2
	reconcileBadInput = 3
)

// reconcileCommand compares the index of the report's tenant with the source counts in the report at path (ADR-0013) and
// writes the result as JSON to out: 0 when every count matches, 1 when one differs, 2 when the index cannot be counted
// and 3 when the report cannot be read. The report is gathered from the source services outside this command (#213).
func reconcileCommand(ctx context.Context, getenv func(string) string, path string, out io.Writer) int {
	f, err := os.Open(path)
	if err != nil {
		slog.ErrorContext(ctx, "reconcile: cannot read the source report", "err", err)
		return reconcileBadInput
	}
	defer func() { _ = f.Close() }()
	src, err := reconcile.ReadSource(f)
	if err != nil {
		slog.ErrorContext(ctx, "reconcile: invalid source report", "err", err)
		return reconcileBadInput
	}
	st, closeDB, err := connect(ctx, getenv)
	if err != nil {
		slog.ErrorContext(ctx, "reconcile: cannot reach the index", "err", err)
		return reconcileFailed
	}
	defer closeDB()
	return reconcileReport(ctx, st, src, out)
}

func reconcileReport(ctx context.Context, c reconcile.Counter, src reconcile.Source, out io.Writer) int {
	res, err := reconcile.Run(ctx, c, src)
	if err != nil {
		slog.ErrorContext(ctx, "reconcile: cannot count the index", "err", err)
		return reconcileFailed
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return reconcileFailed
	}
	if len(res.Diffs) > 0 {
		slog.WarnContext(ctx, "search index counts differ from the sources", "tenantId", res.TenantID, "diffs", len(res.Diffs))
		return reconcileDiffers
	}
	return reconcileMatch
}
