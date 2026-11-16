package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// PI execution records (FR-25.18). When a kiosk finishes a PI sheet the service freezes what was done: for each step of the
// recipe, in document order, what the operator entered or the machine gave, who signed and with what meaning, when, and
// every deviation. The steps are chained: each step's hash covers its own content and the hash before it, so changing,
// removing, adding or reordering a step afterwards changes every hash from there on, and the record's hash covers the
// chain's head. Verification recomputes the chain from what is stored.

// Execution step statuses.
const (
	ExecRecorded     = "recorded"     // the kiosk recorded a message for the step
	ExecAcknowledged = "acknowledged" // a step that collects nothing (text, materials, sequence) was passed
	ExecSkipped      = "skipped"      // a branch jumped over it
	ExecMissing      = "missing"      // the step collects something and nothing was recorded
)

// ExecMessage is a process message the kiosk recorded for a step, as the record builder reads it.
type ExecMessage struct {
	SourceMessageID string
	OccurredAt      time.Time
	Values          []Characteristic
	SignerUser      string
	SignerAuthTime  *time.Time
}

// ExecStep is one step of an execution record. Hash covers PrevHash and every other field.
type ExecStep struct {
	Seq              int              `json:"seq"`
	Key              string           `json:"key"`
	PhasePath        string           `json:"phasePath"`
	Kind             string           `json:"kind"`
	Text             string           `json:"text"`
	Template         *TemplateRef     `json:"template"`
	Status           string           `json:"status"`
	Values           []Characteristic `json:"values"`
	OccurredAt       string           `json:"occurredAt"` // RFC 3339, UTC, empty when nothing was recorded
	SignerUser       string           `json:"signerUser"`
	SignerAuthTime   string           `json:"signerAuthTime"`
	SignatureMeaning string           `json:"signatureMeaning"`
	Comment          string           `json:"comment"`
	Deviation        bool             `json:"deviation"`
	DeviationReasons []string         `json:"deviationReasons"`
	PrevHash         string           `json:"-"`
	Hash             string           `json:"-"`
}

func execTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// StepKey is the key the kiosk gives a step in its process message ids: unit procedure/operation/phase/seq.
func StepKey(up, op, ph string, seq int) string { return fmt.Sprintf("%s/%s/%s/%d", up, op, ph, seq) }

// BuildExecutionSteps lists every step of the recipe in order with what the kiosk recorded for it. messages are those with
// the source id "<recipe id>:<step key>".
func BuildExecutionSteps(recipeID string, doc RecipeDoc, messages []ExecMessage) []ExecStep {
	byKey := map[string]ExecMessage{}
	for _, m := range messages {
		if id, key, ok := strings.Cut(m.SourceMessageID, ":"); ok && strings.EqualFold(id, recipeID) {
			byKey[key] = m
		}
	}
	var out []ExecStep
	for _, up := range doc.Procedure.UnitProcedures {
		for _, op := range up.Operations {
			for _, ph := range op.Phases {
				path := strings.Join([]string{doc.Procedure.Name, up.Name, op.Name, ph.Name}, "/")
				skipped := map[int]bool{}
				for _, in := range ph.Instructions {
					key := StepKey(up.Name, op.Name, ph.Name, in.Seq)
					m, has := byKey[key]
					s := ExecStep{Key: key, PhasePath: path, Kind: in.Category, Text: in.Text, Template: in.Template, Values: []Characteristic{}, DeviationReasons: []string{}}
					switch {
					case has:
						s.Status = ExecRecorded
						s.Values = append(s.Values, m.Values...)
						s.OccurredAt = execTime(m.OccurredAt)
						s.SignerUser = m.SignerUser
						if m.SignerAuthTime != nil {
							s.SignerAuthTime = execTime(*m.SignerAuthTime)
						}
						if in.Category == InstBranch {
							var taken bool
							var target int
							for _, v := range m.Values {
								switch v.Name {
								case "branch-taken":
									taken = v.Value == "true"
								case "branch-target":
									fmt.Sscan(v.Value, &target)
								}
							}
							if taken {
								for n := in.Seq + 1; n < target; n++ {
									skipped[n] = true
								}
							}
						}
					case skipped[in.Seq]:
						s.Status = ExecSkipped
					case passesWithoutData(in):
						s.Status = ExecAcknowledged
					default:
						s.Status = ExecMissing
					}
					s.annotate(in)
					out = append(out, s)
				}
			}
		}
	}
	for i := range out {
		out[i].Seq = i + 1
	}
	return out
}

func passesWithoutData(in Instruction) bool {
	switch in.Category {
	case InstText, InstMaterials, InstSequence:
		return true
	case InstCalculation:
		return in.Expression == ""
	}
	return false
}

// annotate reads comment, signature and deviations out of the recorded values.
func (s *ExecStep) annotate(in Instruction) {
	var primary, expected, tolerance *decimal.Decimal
	for _, v := range s.Values {
		switch v.Name {
		case "comment":
			s.Comment = v.Value
		case "signature-meaning":
			s.SignatureMeaning = v.Value
		case "readout-source":
			if v.Value == "entered-by-hand" {
				s.DeviationReasons = append(s.DeviationReasons, "the machine reading was entered by hand")
			}
		case "expected", "tolerance":
			if d, err := decimal.NewFromString(v.Value); err == nil {
				if v.Name == "expected" {
					expected = &d
				} else {
					tolerance = &d
				}
			}
		default:
			if primary == nil && v.Type == "numeric" && in.Char != nil && v.Name == in.Char.Name {
				if d, err := decimal.NewFromString(v.Value); err == nil {
					primary = &d
				}
			}
			if v.OutOfLimit {
				s.DeviationReasons = append(s.DeviationReasons, v.Name+" is outside its limits")
			}
		}
	}
	if primary != nil && expected != nil && tolerance != nil && primary.Sub(*expected).Abs().GreaterThan(*tolerance) {
		s.DeviationReasons = append(s.DeviationReasons, "the reading is outside its tolerance")
	}
	if s.Status == ExecMissing {
		s.DeviationReasons = append(s.DeviationReasons, "the step was not recorded")
	}
	s.Deviation = len(s.DeviationReasons) > 0
}

// ChainSeed is the hash a record's first step chains from: it binds the chain to its execution and recipe.
func ChainSeed(executionID, recipeID string) string {
	sum := sha256.Sum256([]byte("passage.pi-execution.v1|" + strings.ToLower(executionID) + "|" + strings.ToLower(recipeID)))
	return hex.EncodeToString(sum[:])
}

// StepHash is the SHA-256 of the previous hash and the canonical JSON of the step.
func StepHash(prev string, s ExecStep) string {
	body, _ := json.Marshal(s) // struct field order is fixed; PrevHash and Hash are not part of it
	sum := sha256.Sum256(append([]byte(prev+"|"), body...))
	return hex.EncodeToString(sum[:])
}

// ChainSteps sets PrevHash and Hash on every step, from the seed, and returns the head hash.
func ChainSteps(seed string, steps []ExecStep) string {
	prev := seed
	for i := range steps {
		steps[i].PrevHash = prev
		steps[i].Hash = StepHash(prev, steps[i])
		prev = steps[i].Hash
	}
	return prev
}

// ExecHeader is what the record hash covers besides the chain.
type ExecHeader struct {
	ID           string `json:"id"`
	RecipeID     string `json:"recipeId"`
	OrderID      string `json:"orderId"`
	ResourceID   string `json:"resourceId"`
	SheetCode    string `json:"sheetCode"`
	SheetVersion int    `json:"sheetVersion"`
	ExecutedBy   string `json:"executedBy"`
	StartedAt    string `json:"startedAt"`
	CompletedAt  string `json:"completedAt"`
	StepCount    int    `json:"stepCount"`
	HeadHash     string `json:"headHash"`
}

// RecordHash is the SHA-256 of the canonical header, which includes the head of the chain.
func RecordHash(h ExecHeader) string {
	body, _ := json.Marshal(h)
	sum := sha256.Sum256(append([]byte("passage.pi-record.v1|"), body...))
	return hex.EncodeToString(sum[:])
}

// ExecTime formats a time the way the record hashes it.
func ExecTime(t time.Time) string { return execTime(t) }

// VerifyResult is the outcome of checking a record.
type VerifyResult struct {
	Valid        bool   `json:"valid"`
	CheckedSteps int    `json:"checkedSteps"`
	BrokenAtSeq  *int   `json:"brokenAtSeq"`
	Reason       string `json:"reason"`
	RecordHash   string `json:"recordHash"`
}

// VerifyChain recomputes the chain over the steps as stored (in seq order) and the record hash, and reports the first
// step that does not match.
func VerifyChain(h ExecHeader, storedRecordHash string, steps []ExecStep) VerifyResult {
	res := VerifyResult{RecordHash: storedRecordHash}
	broken := func(seq int, why string) VerifyResult {
		res.BrokenAtSeq, res.Reason = &seq, why
		return res
	}
	prev := ChainSeed(h.ID, h.RecipeID)
	for i, s := range steps {
		if s.Seq != i+1 {
			return broken(i+1, fmt.Sprintf("step %d is missing or out of order", i+1))
		}
		if s.PrevHash != prev {
			return broken(s.Seq, "the step does not chain from the step before it")
		}
		if want := StepHash(prev, s); want != s.Hash {
			return broken(s.Seq, "the step's content does not match its hash")
		}
		prev = s.Hash
		res.CheckedSteps++
	}
	if len(steps) != h.StepCount {
		return broken(len(steps)+1, fmt.Sprintf("the record has %d steps, it was sealed with %d", len(steps), h.StepCount))
	}
	h.HeadHash = prev
	if RecordHash(h) != storedRecordHash {
		return broken(0, "the record header does not match its hash")
	}
	res.Valid = true
	return res
}
