package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/domain"
)

// CodePIExecutionNotFound is SF-0197 (404).
const CodePIExecutionNotFound = "SF-0197"

// PIExecution is the sealed header of a PI sheet run.
type PIExecution struct {
	ID             string    `json:"id"`
	RecipeID       string    `json:"recipeId"`
	OrderID        string    `json:"orderId"`
	ResourceID     string    `json:"resourceId"`
	PISheetID      *string   `json:"piSheetId"`
	SheetCode      *string   `json:"sheetCode"`
	SheetVersion   *int      `json:"sheetVersion"`
	ExecutedBy     string    `json:"executedBy"`
	StartedAt      time.Time `json:"startedAt"`
	CompletedAt    time.Time `json:"completedAt"`
	StepCount      int       `json:"stepCount"`
	DeviationCount int       `json:"deviationCount"`
	HeadHash       string    `json:"headHash"`
	RecordHash     string    `json:"recordHash"`
	CreatedAt      time.Time `json:"createdAt"`
}

// PIExecutionStep is a step of the record with its place in the hash chain.
type PIExecutionStep struct {
	domain.ExecStep
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
}

// PIExecutionDetail is a record with its steps.
type PIExecutionDetail struct {
	PIExecution
	Steps []PIExecutionStep `json:"steps"`
}

// PIExecutionFilter narrows the list.
type PIExecutionFilter struct {
	OrderID  string
	RecipeID string
	Limit    int
}

const piExecCols = `id::text, recipe_id::text, order_id::text, resource_id::text, pi_sheet_id::text, sheet_code, sheet_version, executed_by, started_at, completed_at, step_count, deviation_count, head_hash, record_hash, created_at`

func scanPIExec(row pgx.Row) (PIExecution, error) {
	var e PIExecution
	err := row.Scan(&e.ID, &e.RecipeID, &e.OrderID, &e.ResourceID, &e.PISheetID, &e.SheetCode, &e.SheetVersion, &e.ExecutedBy, &e.StartedAt, &e.CompletedAt, &e.StepCount, &e.DeviationCount, &e.HeadHash, &e.RecordHash, &e.CreatedAt)
	return e, err
}

func (e PIExecution) header() domain.ExecHeader {
	h := domain.ExecHeader{ID: e.ID, RecipeID: e.RecipeID, OrderID: e.OrderID, ResourceID: e.ResourceID, ExecutedBy: e.ExecutedBy,
		StartedAt: domain.ExecTime(e.StartedAt), CompletedAt: domain.ExecTime(e.CompletedAt), StepCount: e.StepCount, DeviationCount: e.DeviationCount, HeadHash: e.HeadHash}
	if e.PISheetID != nil {
		h.SheetID = *e.PISheetID
	}
	if e.SheetCode != nil {
		h.SheetCode = *e.SheetCode
	}
	if e.SheetVersion != nil {
		h.SheetVersion = *e.SheetVersion
	}
	return h
}

// sealExecution freezes the run of a kiosk recipe that was just completed: the steps of its document with the messages the
// kiosk recorded for them, chained and hashed. Called in the transaction that completes the recipe, so a completed kiosk
// recipe always has its record. A recipe that already has one is left as it is.
func sealExecution(ctx context.Context, tx pgx.Tx, tenantID string, c ControlRecipe, actor string) error {
	var have bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from pi_execution where tenant_id = $1 and recipe_id = $2)`, tenantID, c.ID).Scan(&have); err != nil || have {
		return err
	}
	var doc domain.RecipeDoc
	if err := json.Unmarshal(c.Recipe, &doc); err != nil {
		return err
	}
	var sheetID, sheetCode *string
	var sheetVersion *int
	if err := tx.QueryRow(ctx, `select r.pi_sheet_id::text, s.code, s.version from control_recipe r left join pi_sheet s on s.tenant_id = r.tenant_id and s.id = r.pi_sheet_id where r.tenant_id = $1 and r.id = $2`,
		tenantID, c.ID).Scan(&sheetID, &sheetCode, &sheetVersion); err != nil {
		return err
	}
	var started, completed time.Time
	if err := tx.QueryRow(ctx, `select coalesce((select min(at) from control_recipe_event where tenant_id = $1 and recipe_id = $2 and to_status = 'in-execution'), now()), now()`, tenantID, c.ID).Scan(&started, &completed); err != nil {
		return err
	}
	msgs, err := loadExecMessages(ctx, tx, tenantID, c)
	if err != nil {
		return err
	}
	steps := domain.BuildExecutionSteps(c.ID, doc, msgs)
	id := ids.New()
	head := domain.ChainSteps(domain.ChainSeed(id, c.ID), steps)
	exec := PIExecution{ID: id, RecipeID: c.ID, OrderID: c.OrderID, ResourceID: c.ResourceID, PISheetID: sheetID, SheetCode: sheetCode, SheetVersion: sheetVersion,
		ExecutedBy: actor, StartedAt: started, CompletedAt: completed, StepCount: len(steps), HeadHash: head}
	for _, s := range steps {
		if s.Deviation {
			exec.DeviationCount++
		}
	}
	// The deviation count and the sheet are part of what the record hash covers.
	exec.RecordHash = domain.RecordHash(exec.header())
	if _, err := tx.Exec(ctx, `insert into pi_execution (tenant_id, id, recipe_id, order_id, resource_id, pi_sheet_id, sheet_code, sheet_version, executed_by, started_at, completed_at, step_count, deviation_count, head_hash, record_hash)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		tenantID, id, c.ID, c.OrderID, c.ResourceID, sheetID, sheetCode, sheetVersion, actor, started, completed, exec.StepCount, exec.DeviationCount, head, exec.RecordHash); err != nil {
		return err
	}
	for _, s := range steps {
		body, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into pi_execution_step (tenant_id, execution_id, seq, step_key, status, deviation, body, prev_hash, hash) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			tenantID, id, s.Seq, s.Key, s.Status, s.Deviation, body, s.PrevHash, s.Hash); err != nil {
			return err
		}
	}
	return nil
}

// loadExecMessages reads the process messages a kiosk recorded for the steps of a recipe, oldest first.
func loadExecMessages(ctx context.Context, tx pgx.Tx, tenantID string, c ControlRecipe) ([]domain.ExecMessage, error) {
	rows, err := tx.Query(ctx, `select source_message_id, occurred_at, characteristics, coalesce(signer_user, ''), signer_auth_time, received_at from process_message
		where tenant_id = $1 and source = 'kiosk' and order_id = $2 and deleted_at is null and lower(source_message_id) like lower($3) order by occurred_at, id`, tenantID, c.OrderID, c.ID+":%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []domain.ExecMessage
	for rows.Next() {
		var m domain.ExecMessage
		var raw []byte
		if err := rows.Scan(&m.SourceMessageID, &m.OccurredAt, &raw, &m.SignerUser, &m.SignerAuthTime, &m.ReceivedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &m.Values); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func piExecNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodePIExecutionNotFound, "PI execution record not found")
}

// ListPIExecutions returns the records, newest first.
func (s *Postgres) ListPIExecutions(ctx context.Context, f PIExecutionFilter) ([]PIExecution, error) {
	out := []PIExecution{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+piExecCols+` from pi_execution where ($1 = '' or order_id::text = $1) and ($2 = '' or recipe_id::text = $2) order by completed_at desc, id limit $3`, f.OrderID, f.RecipeID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanPIExec(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func loadPIExecution(ctx context.Context, tx pgx.Tx, id string) (PIExecution, []PIExecutionStep, error) {
	e, err := scanPIExec(tx.QueryRow(ctx, `select `+piExecCols+` from pi_execution where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, nil, piExecNotFound()
	}
	if err != nil {
		return e, nil, err
	}
	rows, err := tx.Query(ctx, `select seq, step_key, status, deviation, body, prev_hash, hash from pi_execution_step where execution_id = $1 order by seq`, id)
	if err != nil {
		return e, nil, err
	}
	defer rows.Close()
	steps := []PIExecutionStep{}
	for rows.Next() {
		var seq int
		var key, status string
		var dev bool
		var body []byte
		var st PIExecutionStep
		if err := rows.Scan(&seq, &key, &status, &dev, &body, &st.PrevHash, &st.Hash); err != nil {
			return e, nil, err
		}
		if err := json.Unmarshal(body, &st.ExecStep); err != nil {
			return e, nil, err
		}
		// The columns the lists filter on must agree with the sealed body; a mismatch is shown as a body that does not match.
		if st.ExecStep.Seq != seq || st.ExecStep.Key != key || st.ExecStep.Status != status || st.ExecStep.Deviation != dev {
			st.ExecStep.Seq = -seq
		}
		steps = append(steps, st)
	}
	return e, steps, rows.Err()
}

// GetPIExecution returns a record with its steps.
func (s *Postgres) GetPIExecution(ctx context.Context, id string) (PIExecutionDetail, error) {
	var out PIExecutionDetail
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, steps, err := loadPIExecution(ctx, tx, id)
		out = PIExecutionDetail{PIExecution: e, Steps: steps}
		return err
	})
	return out, err
}

// VerifyPIExecution recomputes the hash chain and the record hash from what is stored.
func (s *Postgres) VerifyPIExecution(ctx context.Context, id string) (domain.VerifyResult, error) {
	var res domain.VerifyResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, steps, err := loadPIExecution(ctx, tx, id)
		if err != nil {
			return err
		}
		plain := make([]domain.ExecStep, len(steps))
		for i, st := range steps {
			plain[i] = st.ExecStep
			plain[i].PrevHash, plain[i].Hash = st.PrevHash, st.Hash
		}
		res = domain.VerifyChain(e.header(), e.RecordHash, plain)
		return nil
	})
	return res, err
}
