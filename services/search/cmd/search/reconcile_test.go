package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BenjaminSRussell/depot/services/search/internal/reconcile"
	"github.com/BenjaminSRussell/depot/services/search/internal/store"
)

const reconcileTenant = "00000000-0000-0000-0000-0000000000f1"

type countsFunc func(context.Context) ([]store.Count, error)

func (f countsFunc) Counts(ctx context.Context) ([]store.Count, error) { return f(ctx) }

func TestReconcileExitsOneOnADifferenceAndPrintsIt(t *testing.T) {
	idx := countsFunc(func(context.Context) ([]store.Count, error) { return []store.Count{{Type: store.TypeParty, N: 2}}, nil })
	var out bytes.Buffer
	src := reconcile.Source{TenantID: reconcileTenant, Counts: []store.Count{{Type: store.TypeParty, N: 3}}}
	if code := reconcileReport(context.Background(), idx, src, &out); code != reconcileDiffers {
		t.Fatalf("exit %d, want %d", code, reconcileDiffers)
	}
	var res reconcile.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Diffs) != 1 || res.Diffs[0] != (reconcile.Diff{Type: store.TypeParty, Index: 2, Source: 3}) {
		t.Fatalf("printed %s", out.String())
	}
	src.Counts[0].N = 2
	if code := reconcileReport(context.Background(), idx, src, &bytes.Buffer{}); code != reconcileMatch {
		t.Fatalf("exit %d on matching counts, want %d", code, reconcileMatch)
	}
}

func TestReconcileRefusesABadReportAndAMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	none := func(string) string { return "" }
	for name, tc := range map[string]struct {
		path string
		want int
	}{
		"missing file": {filepath.Join(dir, "absent.json"), reconcileBadInput},
		"bad report":   {write("bad.json", `{"tenantId": "x"}`), reconcileBadInput},
		"no database":  {write("ok.json", `{"tenantId": "`+reconcileTenant+`", "counts": [{"type": "party", "count": 1}]}`), reconcileFailed},
	} {
		if code := reconcileCommand(context.Background(), none, tc.path, &bytes.Buffer{}); code != tc.want {
			t.Errorf("%s: exit %d, want %d", name, code, tc.want)
		}
	}
}
