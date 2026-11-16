// Package openingimport is the shared part of the cutover opening-balance imports (#49): the batch header and line
// reference tables, the all-or-nothing idempotent store of a batch, the read-back list and the HTTP envelope checks.
// A service writes its own migration (a batch table and a line table named by Tables), its own validation and its own
// domain rows; this package stores the batch under its key, refuses a reused line reference or natural key, and
// reads the batches back with totals recomputed from the stored lines.
package openingimport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// MaxLines is the most lines one batch holds.
const MaxLines = 1000

// KeyPattern is the shape of a batch key.
var KeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

// CurrencyPattern is a 3-letter ISO 4217 code in capitals.
var CurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// ErrKeyConflict is the same batch key sent again with different content.
var ErrKeyConflict = errors.New("batch key already imported with different content")

// TakenError names the line references an earlier batch already imported (with that batch's key) and the natural keys
// (asset numbers, production order numbers) the legal entity already holds.
type TakenError struct {
	References map[string]string
	Keys       map[string]bool
}

func (e *TakenError) Error() string { return "references or numbers already in use" }

// Batch is a stored batch. Totals are summed from its stored lines.
type Batch struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	BatchKey      string            `json:"batchKey"`
	LineCount     int               `json:"lineCount"`
	Totals        map[string]string `json:"totals"`
	ImportedBy    string            `json:"importedBy"`
	ImportedAt    time.Time         `json:"importedAt"`
}

// Line is one line of a batch as the reference table keeps it: the legacy reference and the named amounts that add up
// to the batch totals (for example "cost.USD").
type Line struct {
	Reference string
	Measures  map[string]decimal.Decimal
}

// Input is one batch.
type Input struct {
	LegalEntityID string
	BatchKey      string
	ContentHash   string
	ImportedBy    string
	// Totals are the declared control totals, already checked against the lines.
	Totals map[string]string
	Lines  []Line
}

// Hooks are the service's part of a batch store.
type Hooks struct {
	// Taken returns which natural keys of the batch the legal entity already holds; nil means none are checked.
	Taken func(ctx context.Context, tx pgx.Tx) (map[string]bool, error)
	// Insert stores the domain rows of the batch.
	Insert func(ctx context.Context, tx pgx.Tx, batchID string) error
}

// Tables names a service's batch and line tables. Both carry tenant_id first and are held to the tenant and the legal
// entity by row-level security (see the migration of the service). Batch has id, legal_entity_id, batch_key,
// content_hash, line_count, control_totals, imported_by, imported_at; Line has legal_entity_id, reference, batch_id and
// measures (jsonb of decimal strings).
type Tables struct {
	Batch string
	Line  string
}

// Import stores a batch all or nothing. The same key with the same content is a replay (created false); the same key
// with other content is ErrKeyConflict; a taken reference or natural key is *TakenError.
func (t Tables) Import(ctx context.Context, pool *pgxpool.Pool, in Input, h Hooks) (Batch, bool, error) {
	out, created, err := t.importOnce(ctx, pool, in, h)
	if isRace(err) {
		out, created, err = t.importOnce(ctx, pool, in, h)
		if isRace(err) {
			err = &TakenError{References: map[string]string{}, Keys: map[string]bool{}}
		}
	}
	return out, created, err
}

func isRace(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func (t Tables) importOnce(ctx context.Context, pool *pgxpool.Pool, in Input, h Hooks) (Batch, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Batch
	created := false
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || ':' || $2 || ':' || $3 || ':' || $4, 0))`, t.Batch, p.TenantID, in.LegalEntityID, in.BatchKey); err != nil {
			return err
		}
		var hash string
		err := tx.QueryRow(ctx, `select content_hash from `+t.Batch+` where legal_entity_id = $1::uuid and batch_key = $2`, in.LegalEntityID, in.BatchKey).Scan(&hash)
		if err == nil {
			if hash != in.ContentHash {
				return ErrKeyConflict
			}
			out, err = t.one(ctx, tx, in.LegalEntityID, in.BatchKey)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := t.rejectTaken(ctx, tx, in, h); err != nil {
			return err
		}
		totals, _ := json.Marshal(in.Totals)
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into `+t.Batch+` (tenant_id, id, legal_entity_id, batch_key, content_hash, line_count, control_totals, imported_by)
			values ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8)`, p.TenantID, id, in.LegalEntityID, in.BatchKey, in.ContentHash, len(in.Lines), totals, in.ImportedBy); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, l := range in.Lines {
			m := make(map[string]string, len(l.Measures))
			for k, v := range l.Measures {
				m[k] = v.String()
			}
			raw, _ := json.Marshal(m)
			batch.Queue(`insert into `+t.Line+` (tenant_id, legal_entity_id, reference, batch_id, measures) values ($1, $2::uuid, $3, $4::uuid, $5)`, p.TenantID, in.LegalEntityID, l.Reference, id, raw)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		if h.Insert != nil {
			if err := h.Insert(ctx, tx, id); err != nil {
				return err
			}
		}
		created = true
		out, err = t.one(ctx, tx, in.LegalEntityID, in.BatchKey)
		return err
	})
	return out, created, err
}

func (t Tables) one(ctx context.Context, tx pgx.Tx, legalEntityID, batchKey string) (Batch, error) {
	batches, err := t.list(ctx, tx, legalEntityID, batchKey, 1, time.Time{}, "")
	if err != nil {
		return Batch{}, err
	}
	if len(batches) == 0 {
		return Batch{}, pgx.ErrNoRows
	}
	return batches[0], nil
}

func (t Tables) rejectTaken(ctx context.Context, tx pgx.Tx, in Input, h Hooks) error {
	refs := make([]string, len(in.Lines))
	for i, l := range in.Lines {
		refs[i] = l.Reference
	}
	taken := &TakenError{References: map[string]string{}, Keys: map[string]bool{}}
	rows, err := tx.Query(ctx, `select l.reference, b.batch_key from `+t.Line+` l join `+t.Batch+` b on b.tenant_id = l.tenant_id and b.id = l.batch_id
		where l.legal_entity_id = $1::uuid and l.reference = any($2)`, in.LegalEntityID, refs)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ref, key string
		if err := rows.Scan(&ref, &key); err != nil {
			rows.Close()
			return err
		}
		taken.References[ref] = key
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if h.Taken != nil {
		if taken.Keys, err = h.Taken(ctx, tx); err != nil {
			return err
		}
		if taken.Keys == nil {
			taken.Keys = map[string]bool{}
		}
	}
	if len(taken.References) > 0 || len(taken.Keys) > 0 {
		return taken
	}
	return nil
}

// List returns a legal entity's batches, newest first, optionally one batch key, after the (afterAt, afterID) position.
func (t Tables) List(ctx context.Context, pool *pgxpool.Pool, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]Batch, error) {
	var out []Batch
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		out, err = t.list(ctx, tx, legalEntityID, batchKey, min(max(limit, 1), 201), afterAt, afterID)
		return err
	})
	return out, err
}

func (t Tables) list(ctx context.Context, tx pgx.Tx, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]Batch, error) {
	var cursorAt, cursorID any
	if !afterAt.IsZero() {
		cursorAt, cursorID = afterAt, afterID
	}
	rows, err := tx.Query(ctx, `select b.id::text, b.legal_entity_id::text, b.batch_key, b.imported_by, b.imported_at, b.line_count, coalesce(m.key, ''), coalesce(m.total, 0)::text
		from (select * from `+t.Batch+` where legal_entity_id = $1::uuid and ($2 = '' or batch_key = $2)
			and ($4::timestamptz is null or (imported_at, id) < ($4::timestamptz, $5::uuid)) order by imported_at desc, id desc limit $3) b
		left join lateral (select kv.key, sum(kv.value::numeric) as total from `+t.Line+` l, jsonb_each_text(l.measures) kv
			where l.tenant_id = b.tenant_id and l.batch_id = b.id group by kv.key) m on true
		order by b.imported_at desc, b.id desc, m.key`, legalEntityID, batchKey, limit, cursorAt, cursorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Batch{}
	for rows.Next() {
		var b Batch
		var key, total string
		if err := rows.Scan(&b.ID, &b.LegalEntityID, &b.BatchKey, &b.ImportedBy, &b.ImportedAt, &b.LineCount, &key, &total); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != b.ID {
			b.Totals = map[string]string{}
			out = append(out, b)
		}
		if key != "" {
			v, err := decimal.NewFromString(total)
			if err != nil {
				return nil, err
			}
			out[len(out)-1].Totals[key] = v.String()
		}
	}
	return out, rows.Err()
}

// Hash is the content hash of a batch: the legal entity, the declared totals and the lines in reference order, as JSON.
func Hash(legalEntityID string, totals map[string]string, lines any) string {
	raw, _ := json.Marshal(struct {
		LegalEntityID string
		Totals        map[string]string
		Lines         any
	}{legalEntityID, totals, lines})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ParseTotals reads declared control totals: a name and a decimal.
func ParseTotals(raw map[string]string) (map[string]string, map[string]string) {
	out := map[string]string{}
	bad := map[string]string{}
	for name, v := range raw {
		d, err := decimal.NewFromString(v)
		if name == "" || err != nil {
			bad["controlTotals."+name] = "must be a decimal"
			continue
		}
		out[name] = d.String()
	}
	return out, bad
}

// SumTotals adds the lines' measures per name.
func SumTotals(lines []Line) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	for _, l := range lines {
		for k, v := range l.Measures {
			out[k] = out[k].Add(v)
		}
	}
	return out
}

// Mismatch compares the declared totals with the sums of the lines, name by name, both ways.
func Mismatch(declared map[string]string, actual map[string]decimal.Decimal) map[string]string {
	bad := map[string]string{}
	for name, raw := range declared {
		want, _ := decimal.NewFromString(raw)
		if !actual[name].Equal(want) {
			bad["controlTotals."+name] = fmt.Sprintf("declared %s but the lines add up to %s", want.String(), actual[name].String())
		}
	}
	for name, got := range actual {
		if _, ok := declared[name]; !ok && !got.IsZero() {
			bad["controlTotals."+name] = "no total declared; the lines add up to " + got.String()
		}
	}
	return bad
}

// Envelope checks what every batch request carries: the legal entity (in the caller's scope), the batch key, the line count and
// the control totals. A non-nil problem is to be sent as is; fields are validation messages for the request fields.
func Envelope(caller tenant.Principal, legalEntityID, batchKey string, lines int, totals map[string]string) (*httpx.Problem, map[string]string) {
	fields := map[string]string{}
	if !ids.Valid(legalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	} else if !tenant.InLegalEntityScope(caller, legalEntityID) {
		return httpx.NewProblem(http.StatusNotFound, "COM-0404", "legal entity not found"), nil
	}
	if !KeyPattern.MatchString(batchKey) {
		fields["batchKey"] = "is required: 1 to 100 letters, digits, dots, dashes, colons or underscores"
	}
	if lines == 0 || lines > MaxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines; send larger snapshots in several batches", MaxLines)
	}
	if len(totals) == 0 {
		fields["controlTotals"] = "is required: the batch totals the lines must add up to"
	}
	return nil, fields
}

// Row is the key of a field of line i (0-based) in the errors map: lines[1].field.
func Row(i int, field string) string { return "lines[" + strconv.Itoa(i+1) + "]." + field }

// Counters are the outcome counters of one import route.
type Counters struct {
	Imported atomic.Int64
	Replayed atomic.Int64
	Refused  atomic.Int64
	Lines    atomic.Int64
}

// Write renders the counters as Prometheus text under the name prefix (for example "assets_opening").
func (c *Counters) Write(w http.ResponseWriter, prefix, what string) {
	_, _ = fmt.Fprintf(w, "# HELP %s_batches_total Opening %s batches by outcome.\n# TYPE %s_batches_total counter\n%s_batches_total{outcome=\"imported\"} %d\n%s_batches_total{outcome=\"replayed\"} %d\n%s_batches_total{outcome=\"refused\"} %d\n",
		prefix, what, prefix, prefix, c.Imported.Load(), prefix, c.Replayed.Load(), prefix, c.Refused.Load())
	_, _ = fmt.Fprintf(w, "# HELP %s_lines_total Opening %s lines stored by imported batches.\n# TYPE %s_lines_total counter\n%s_lines_total %d\n", prefix, what, prefix, prefix, c.Lines.Load())
}

// Refuse sends a refused batch: a problem with the field messages and the structured row errors.
func (c *Counters) Refuse(w http.ResponseWriter, r *http.Request, status int, code, detail string, fields map[string]string, rows ...httpx.RowError) {
	c.Refused.Add(1)
	p := httpx.NewProblem(status, code, detail)
	p.Errors = fields
	p.RowErrors = rows
	httpx.Fail(w, r, p)
}

// Answer sends the stored batch: 201 for a new one, 200 with Idempotent-Replayed for a replay.
func (c *Counters) Answer(w http.ResponseWriter, b Batch, created bool) {
	if created {
		c.Imported.Add(1)
		c.Lines.Add(int64(b.LineCount))
		httpx.JSON(w, http.StatusCreated, b)
		return
	}
	c.Replayed.Add(1)
	w.Header().Set("Idempotent-Replayed", "true")
	httpx.JSON(w, http.StatusOK, b)
}

// ListQuery is a parsed read-back request.
type ListQuery struct {
	LegalEntityID string
	BatchKey      string
	Limit         int
	AfterAt       time.Time
	AfterID       string
}

// ParseList reads legal_entity, batch_key, limit and cursor of a read-back request.
func ParseList(r *http.Request, caller tenant.Principal) (ListQuery, map[string]string) {
	q := ListQuery{LegalEntityID: r.URL.Query().Get("legal_entity"), BatchKey: r.URL.Query().Get("batch_key"), Limit: 50}
	if !ids.Valid(q.LegalEntityID) || !tenant.InLegalEntityScope(caller, q.LegalEntityID) {
		return q, map[string]string{"legal_entity": "must be a legal entity UUID in your scope"}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			return q, map[string]string{"limit": "must be a whole number from 1 to 200"}
		}
		q.Limit = n
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		at, id, found := strings.Cut(string(decoded), "|")
		when, perr := time.Parse(time.RFC3339Nano, at)
		if err != nil || !found || perr != nil || !ids.Valid(id) {
			return q, map[string]string{"cursor": "is not a cursor this list returned"}
		}
		q.AfterAt, q.AfterID = when, id
	}
	return q, nil
}

// WriteList sends one page of batches fetched with Limit+1.
func WriteList(w http.ResponseWriter, q ListQuery, batches []Batch, asOf time.Time) {
	next := ""
	if len(batches) > q.Limit {
		batches = batches[:q.Limit]
		last := batches[q.Limit-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.ImportedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": batches, "nextCursor": next, "asOf": asOf.UTC().Format(time.RFC3339)})
}

// DuplicateReferences returns the row errors for line references that repeat or are blank.
func DuplicateReferences(refs []string, bad map[string]string) {
	seen := map[string]int{}
	for i, ref := range refs {
		switch {
		case ref == "" || len(ref) > 100:
			bad[Row(i, "reference")] = "is required, at most 100 characters"
		default:
			if first, dup := seen[ref]; dup {
				bad[Row(i, "reference")] = fmt.Sprintf("appears again; first at line %d", first)
			} else {
				seen[ref] = i + 1
			}
		}
	}
}

// SortedKeys returns the keys of m in order.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
