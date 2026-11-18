// Package reconcile compares the search index's record counts with the source services' (ADR-0013): the index is only as
// complete as the events that reach it, so a source whose events stop arriving (no outbox connector, a dead-lettered
// type, a consumer that is off) shows up as a count that falls behind.
//
// The source counts come from a report, one per tenant (ReadSource): for each record type, and for a legal-entity scoped
// type each legal entity, the number of records the source would have the index hold. Gathering that report live from the
// source services is #213; `search reconcile <report.json>` runs the comparison.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/search/internal/index"
	"github.com/BenjaminSRussell/depot/services/search/internal/store"
)

// Types are the record types a source report may count: every type the index holds.
var Types = []string{store.TypeParty, store.TypeProduct, store.TypePurchaseOrder, store.TypeReceipt, store.TypeVendorInvoice,
	store.TypeSalesOrder, store.TypePackingSlip, store.TypeCustomerInvoice, store.TypeProductionOrder, store.TypeWorker}

// Source is one tenant's source counts.
type Source struct {
	TenantID string        `json:"tenantId"`
	Counts   []store.Count `json:"counts"`
}

// ReadSource reads and checks a source report: a tenant id, and per known record type a count that is not negative, with
// a legal entity exactly for the scoped types, each (type, legal entity) once.
func ReadSource(r io.Reader) (Source, error) {
	var src Source
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&src); err != nil {
		return Source{}, fmt.Errorf("source report: %w", err)
	}
	if !ids.Valid(src.TenantID) {
		return Source{}, errors.New("source report: tenantId must be a UUID")
	}
	seen := map[[2]string]bool{}
	for i, c := range src.Counts {
		switch {
		case !slices.Contains(Types, c.Type):
			return Source{}, fmt.Errorf("source report: counts[%d]: unknown record type %q", i, c.Type)
		case store.LegalEntityScoped(c.Type) && !ids.Valid(c.LegalEntityID):
			return Source{}, fmt.Errorf("source report: counts[%d]: %s is counted per legal entity; legalEntityId must be a UUID", i, c.Type)
		case !store.LegalEntityScoped(c.Type) && c.LegalEntityID != "":
			return Source{}, fmt.Errorf("source report: counts[%d]: %s is tenant-wide and has no legalEntityId", i, c.Type)
		case c.N < 0:
			return Source{}, fmt.Errorf("source report: counts[%d]: a negative count", i)
		}
		src.Counts[i].LegalEntityID = strings.ToLower(c.LegalEntityID)
		key := [2]string{c.Type, src.Counts[i].LegalEntityID}
		if seen[key] {
			return Source{}, fmt.Errorf("source report: counts[%d]: %s %s is counted twice", i, c.Type, c.LegalEntityID)
		}
		seen[key] = true
	}
	return src, nil
}

// Counter counts the index rows the context's principal may see (store.Store.Counts).
type Counter interface {
	Counts(ctx context.Context) ([]store.Count, error)
}

// Diff is a (type, legal entity) whose index count differs from its source count.
type Diff struct {
	Type          string `json:"type"`
	LegalEntityID string `json:"legalEntityId,omitempty"`
	Index         int64  `json:"index"`
	Source        int64  `json:"source"`
}

// Result is the outcome of one tenant's reconciliation.
type Result struct {
	TenantID string `json:"tenantId"`
	// Compared counts the (type, legal entity) pairs compared; Diffs are those that differ.
	Compared int    `json:"compared"`
	Diffs    []Diff `json:"diffs"`
}

// Run counts the index of the report's tenant and compares it with the report. It counts as the index consumer's service
// principal, which reaches one legal entity at a time: the tenant-wide types once, and the scoped types once per legal
// entity the report names. Only the types the report counts are compared, and for a scoped type only the legal entities
// it names (the report lists a legal entity the source has no record in with count 0).
func Run(ctx context.Context, c Counter, src Source) (Result, error) {
	res := Result{TenantID: src.TenantID, Diffs: []Diff{}}
	var entities []string
	for _, sc := range src.Counts {
		if sc.LegalEntityID != "" && !slices.Contains(entities, sc.LegalEntityID) {
			entities = append(entities, sc.LegalEntityID)
		}
	}
	indexed := map[[2]string]int64{}
	for _, le := range append([]string{""}, entities...) {
		p, err := tenant.ServicePrincipal(index.ConsumerName, src.TenantID, le)
		if err != nil {
			return Result{}, err
		}
		counts, err := c.Counts(tenant.With(ctx, p))
		if err != nil {
			return Result{}, fmt.Errorf("count the index for legal entity %q: %w", le, err)
		}
		for _, ic := range counts {
			// Each pass keeps what it alone sees: the tenant-wide types from the first, one entity's scoped rows from the rest.
			if store.LegalEntityScoped(ic.Type) == (le != "") && (le == "" || strings.EqualFold(ic.LegalEntityID, le)) {
				indexed[[2]string{ic.Type, strings.ToLower(ic.LegalEntityID)}] = ic.N
			}
		}
	}
	keys := map[[2]string]int64{}
	for _, sc := range src.Counts {
		keys[[2]string{sc.Type, sc.LegalEntityID}] = sc.N
	}
	sorted := make([][2]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	slices.SortFunc(sorted, func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	for _, k := range sorted {
		res.Compared++
		if indexed[k] != keys[k] {
			res.Diffs = append(res.Diffs, Diff{Type: k[0], LegalEntityID: k[1], Index: indexed[k], Source: keys[k]})
		}
	}
	return res, nil
}
