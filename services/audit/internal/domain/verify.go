package domain

import "time"

// Kinds of a break in the change chain (AUD-API-4). A break is evidence of tampering: a value changed after it was
// written, a row removed, inserted or reordered, or an anchored head that the chain no longer holds.
const (
	// BreakHash: the row's hash does not recompute from its stored values.
	BreakHash = "hash-mismatch"
	// BreakLink: the row's prevHash is not the hash of the row before it.
	BreakLink = "link-mismatch"
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
)

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
	// Head is the stored hash of the last row walked (Genesis for an empty chain).
	Head string
	// Breaks counts the tampering breaks; FirstBreak is the first in chain order (nil when verified).
	Breaks     int64
	FirstBreak *Break
	// LegacyPrecision and LegacyFork count the known breaks of legacy rows.
	LegacyPrecision int64
	LegacyFork      int64
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
}

// NewChangeWalker starts a walk. fromStart says the first row is the tenant's first, which must link to Genesis; a
// walk over a later range trusts the first row's prevHash. anchors maps an anchored head seq to its hash; pass those in
// the walked range (and, for a walk to the end of the chain, those after it).
func NewChangeWalker(fromStart bool, anchors map[int64]string) *ChangeWalker {
	return &ChangeWalker{
		status:    ChangeChainStatus{Verified: true, Head: Genesis},
		fromStart: fromStart, prev: Genesis, anchors: anchors, seenAnchor: map[int64]bool{},
	}
}

// Next checks one row against the chain so far.
func (w *ChangeWalker) Next(r ChainRow) {
	if w.status.Entries == 0 {
		w.status.FromSeq = r.Seq
		if !w.fromStart {
			w.prev = r.PrevHash
		}
	}
	w.status.Entries++
	w.status.ToSeq = r.Seq
	linked := r.PrevHash == w.prev
	hashed := hashes(r)
	switch {
	case linked && hashed:
	case linked && r.IsLegacy() && legacyPrecision(r):
		w.status.LegacyPrecision++
	case !linked && w.last != nil && r.IsLegacy() && w.last.IsLegacy() && r.PrevHash == w.last.PrevHash && (hashed || legacyPrecision(r)):
		w.status.LegacyFork++
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
	w.prev = r.Hash
	w.status.Head = r.Hash
	row := r
	w.last = &row
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
