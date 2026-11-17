package domain

import (
	"strings"
	"time"
)

// Kinds of a break in the change chain (AUD-API-4). A break is a row that does not verify: a value changed after it
// was written, a row removed, inserted or reordered, an anchored head that the chain no longer holds, or a fork.
const (
	// BreakHash: the row's hash does not recompute from its stored values.
	BreakHash = "hash-mismatch"
	// BreakLink: the row's prevHash is not the hash of the row before it, nor of any recent earlier row.
	BreakLink = "link-mismatch"
	// BreakFork: the row recomputes but links to an earlier row than the one before it: two writers read the same
	// head. That is what a replica reading the head under a narrower scope (a B-06 image after migration 0009, during a
	// mixed rollout or after a rollback) writes. It is still a break, but diagnose it as a fork before tampering
	// (runbook audit-chain-verify-failure).
	BreakFork = "fork"
	// BreakAnchor: a daily anchor names a head (seq and hash) the chain no longer holds.
	BreakAnchor = "anchor-mismatch"
)

// Known breaks of rows written before B-06, which are reported apart and do not fail verification.
const (
	// KnownLegacyPrecision: a legacy row hashed over an occurredAt with a sub-microsecond part, which timestamptz does
	// not keep. Its hash is recovered by trying the 999 nanosecond offsets the stored microsecond value can hide; a
	// legacy row whose hash no offset recovers is a BreakHash.
	KnownLegacyPrecision = "legacy-precision"
	// KnownLegacyFork: two legacy rows linked to the same predecessor, which unserialised appends before B-06 could
	// write. The second row's own hash must still recompute.
	KnownLegacyFork = "legacy-fork"
	// KnownLegacyAmbiguous: a legacy row with a "|" in a field. The legacy payload joins the fields with "|", so a
	// "|" moved between two neighbouring fields leaves the hash unchanged: the row recomputes, but its field
	// boundaries are not proven.
	KnownLegacyAmbiguous = "legacy-ambiguous"
)

// forkWindow is how many recent row hashes the walker remembers to tell a fork from a broken link. A fork links to a
// row written moments before (the head another writer had just read), so a short window finds it while the memory
// of a walk stays bounded.
const forkWindow = 10_000

// ChainRow is one stored change-chain row in seq order.
type ChainRow struct {
	Seq int64
	ID  string
	Entry
}

// Break locates a break by the row's seq and id.
type Break struct {
	Seq  int64  `json:"seq"`
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind"`
}

// ChangeChainStatus is the result of walking the change chain of one tenant, or a seq range of it.
type ChangeChainStatus struct {
	Verified bool
	Entries  int64
	FromSeq  int64
	ToSeq    int64
	// StartPrevHash is the prevHash the walk started from: Genesis for a walk from the tenant's first row, else the
	// first row's own prevHash, trusted. A client chaining pages checks it against the previous page's Head.
	StartPrevHash string
	// Head is the stored hash of the last row walked (Genesis for an empty chain).
	Head string
	// Breaks counts the breaks; FirstBreak is the first in chain order (nil when verified).
	Breaks     int64
	FirstBreak *Break
	// LegacyPrecision, LegacyFork and LegacyAmbiguous count the known breaks of legacy rows.
	LegacyPrecision int64
	LegacyFork      int64
	LegacyAmbiguous int64
	// AnchorsChecked counts the anchors whose head lies in the walked range; AnchorsMismatched those it does not hold.
	AnchorsChecked    int64
	AnchorsMismatched int64
}

// ChangeWalker verifies the change chain one row at a time, so a large tenant is never held in memory. After a break
// it goes on from the stored hash of the broken row, so one changed row is one break and later rows are judged on
// their own.
type ChangeWalker struct {
	status     ChangeChainStatus
	fromStart  bool
	prev       string
	last       *ChainRow
	anchors    map[int64]string
	seenAnchor map[int64]bool
	recent     map[string]int
	ring       []string
}

// NewChangeWalker starts a walk. fromStart says the first row is the tenant's first, which must link to Genesis; a
// walk over a later range trusts the first row's prevHash. anchors maps an anchored head seq to its hash; pass those in
// the walked range (and, for a walk to the end of the chain, those after it).
func NewChangeWalker(fromStart bool, anchors map[int64]string) *ChangeWalker {
	return &ChangeWalker{
		status:    ChangeChainStatus{Verified: true, Head: Genesis, StartPrevHash: Genesis},
		fromStart: fromStart, prev: Genesis, anchors: anchors, seenAnchor: map[int64]bool{}, recent: map[string]int{},
	}
}

// Next checks one row against the chain so far.
func (w *ChangeWalker) Next(r ChainRow) {
	if w.status.Entries == 0 {
		w.status.FromSeq = r.Seq
		if !w.fromStart {
			w.prev = r.PrevHash
		}
		w.status.StartPrevHash = w.prev
	}
	w.status.Entries++
	w.status.ToSeq = r.Seq
	linked := r.PrevHash == w.prev
	recomputes := hashes(r)
	precision := false
	if !recomputes && r.IsLegacy() {
		precision = legacyPrecision(r)
	}
	self := recomputes || precision
	switch {
	case linked && recomputes:
		if r.IsLegacy() && ambiguous(r.Change) {
			w.status.LegacyAmbiguous++
		}
	case linked && precision:
		w.status.LegacyPrecision++
		if ambiguous(r.Change) {
			w.status.LegacyAmbiguous++
		}
	case !linked && w.last != nil && r.IsLegacy() && w.last.IsLegacy() && r.PrevHash == w.last.PrevHash && self:
		w.status.LegacyFork++
	case !linked && self && w.recent[r.PrevHash] > 0:
		w.fail(r, BreakFork)
	case !linked:
		w.fail(r, BreakLink)
	default:
		w.fail(r, BreakHash)
	}
	if want, ok := w.anchors[r.Seq]; ok {
		w.seenAnchor[r.Seq] = true
		w.status.AnchorsChecked++
		if want != r.Hash {
			w.status.AnchorsMismatched++
			w.fail(r, BreakAnchor)
		}
	}
	w.remember(r.Hash)
	w.prev = r.Hash
	w.status.Head = r.Hash
	row := r
	w.last = &row
}

func (w *ChangeWalker) remember(hash string) {
	w.recent[hash]++
	w.ring = append(w.ring, hash)
	if len(w.ring) > forkWindow {
		old := w.ring[0]
		w.ring = w.ring[1:]
		if w.recent[old]--; w.recent[old] <= 0 {
			delete(w.recent, old)
		}
	}
}

// Boundary checks the first row after a page that stopped (the next page's first row, which that page will trust):
// it must link to the last row walked, so rows removed exactly at a page boundary are a break of this page.
func (w *ChangeWalker) Boundary(next ChainRow) {
	if w.status.Entries > 0 && next.PrevHash != w.prev {
		w.fail(next, BreakLink)
	}
}

// DropAnchorsAfter forgets the anchors whose head lies after seq: a walk that stopped there (a page of a longer
// range) cannot judge them.
func (w *ChangeWalker) DropAnchorsAfter(seq int64) {
	for s := range w.anchors {
		if s > seq {
			delete(w.anchors, s)
		}
	}
}

func ambiguous(c Change) bool {
	for _, v := range []string{c.Entity, c.RecordID, c.Field, c.OldValue, c.NewValue, c.UserID, c.TraceID} {
		if strings.Contains(v, "|") {
			return true
		}
	}
	return false
}

// Status ends the walk: an anchor whose head row was never walked (the row is gone, or the chain was cut short) is
// a break.
func (w *ChangeWalker) Status() ChangeChainStatus {
	s := w.status
	var missing int64
	for seq := range w.anchors {
		if seq <= 0 || w.seenAnchor[seq] {
			continue
		}
		s.AnchorsChecked++
		s.AnchorsMismatched++
		s.Breaks++
		if missing == 0 || seq < missing {
			missing = seq
		}
	}
	if s.FirstBreak == nil && missing > 0 {
		s.FirstBreak = &Break{Seq: missing, Kind: BreakAnchor}
	}
	s.Verified = s.Breaks == 0
	return s
}

func (w *ChangeWalker) fail(r ChainRow, kind string) {
	w.status.Breaks++
	if w.status.FirstBreak == nil {
		w.status.FirstBreak = &Break{Seq: r.Seq, ID: r.ID, Kind: kind}
	}
}

func hashes(r ChainRow) bool {
	want := Hash(r.PrevHash, r.Change)
	return want != "" && want == r.Hash
}

// legacyPrecision reports whether a legacy row's hash recomputes for one of the sub-microsecond parts its stored
// occurredAt may have lost (Postgres timestamptz truncates to microseconds).
func legacyPrecision(r ChainRow) bool {
	c := r.Change
	for ns := 1; ns < 1000; ns++ {
		c.OccurredAt = r.OccurredAt.Add(time.Duration(ns))
		if Hash(r.PrevHash, c) == r.Hash {
			return true
		}
	}
	return false
}
