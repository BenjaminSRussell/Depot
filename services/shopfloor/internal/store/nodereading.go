package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/domain"
)

// CodeNoReading is SF-0196 (404): no connector has reported a recent value for the node.
const CodeNoReading = "SF-0196"

// NodeReadingIn is one value the connector read from a node.
type NodeReadingIn struct {
	Node     string
	Value    decimal.Decimal
	NodeType string
	At       time.Time
}

// NodeReading is the reading a machine-readout step was given.
type NodeReading struct {
	Node       string          `json:"node"`
	Value      decimal.Decimal `json:"value"`
	NodeType   string          `json:"nodeType"`
	ReadAt     time.Time       `json:"readAt"`
	AgeSeconds int             `json:"ageSeconds"`
	EdgeID     string          `json:"-"`
	ClaimID    string          `json:"-"`
	receivedAt time.Time
}

// ReportNodeReadings records readings from an edge node's connector (the bound connector only). A reading older than the
// stored one for the same node is ignored.
func (s *Postgres) ReportNodeReadings(ctx context.Context, edgeID, subject string, in []NodeReadingIn) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := edgeConnector(ctx, tx, p.TenantID, edgeID, subject, false); err != nil {
			return err
		}
		for _, r := range in {
			if _, err := tx.Exec(ctx, `insert into node_reading (tenant_id, edge_id, node, value, node_type, read_at) values ($1, $2, $3, $4, $5, $6)
				on conflict (tenant_id, edge_id, node) do update set value = excluded.value, node_type = excluded.node_type, read_at = excluded.read_at, received_at = now()
				where node_reading.read_at < excluded.read_at`, p.TenantID, edgeID, r.Node, r.Value, r.NodeType, r.At); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClaimReadout hands a kiosk the freshest reading of the node a machine-readout step reads and records it as the step's
// claim: the sealed record names the edge node and the time of the value, and a value reported as read from the machine
// must equal it. The reading is judged by when the service received it (not by the connector's clock) and comes from an
// edge node at a site of the recipe's resource (a resource with no machine at a site reads from any edge node of the tenant).
// Once a message is recorded for the step the claim no longer changes. SF-0196 when there is no recent reading.
func (s *Postgres) ClaimReadout(ctx context.Context, recipeID, stepKey string, maxAge time.Duration, now time.Time) (NodeReading, error) {
	p, _ := tenant.FromContext(ctx)
	var out NodeReading
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := lockRecipe(ctx, tx, p.TenantID, recipeID)
		if err != nil {
			return err
		}
		if c.Destination != domain.DestKiosk {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecipeDestination, "this control recipe goes to a process control system")
		}
		var doc domain.RecipeDoc
		if err := json.Unmarshal(c.Recipe, &doc); err != nil {
			return err
		}
		in, ok := doc.StepByKey(stepKey)
		if !ok || in.Category != domain.InstReadout || in.Node == "" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeStepMessage, "the recipe has no machine readout step with that key")
		}
		var stored bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from process_message where tenant_id = $1 and source = 'kiosk' and lower(source_message_id) = lower($2))`, p.TenantID, c.ID+":"+stepKey).Scan(&stored); err != nil {
			return err
		}
		scan := func(row pgx.Row) error {
			return row.Scan(&out.Node, &out.Value, &out.NodeType, &out.ReadAt, &out.EdgeID, &out.ClaimID, &out.receivedAt)
		}
		if stored { // the value the record holds was fixed when it was recorded
			err := scan(tx.QueryRow(ctx, `select node, value, '', read_at, edge_id::text, id::text, claimed_at from pi_readout_claim where tenant_id = $1 and recipe_id = $2 and step_key = $3`, p.TenantID, c.ID, stepKey))
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusNotFound, CodeNoReading, "no machine reading was claimed for this step")
			}
			return err
		}
		var edge string
		var typ string
		var val decimal.Decimal
		var readAt, receivedAt time.Time
		err = tx.QueryRow(ctx, `select r.edge_id::text, r.value, r.node_type, r.read_at, r.received_at from node_reading r
			where r.tenant_id = $1 and r.node = $2 and r.received_at >= $3
			  and (not exists (select 1 from machine m join edge_node e on e.tenant_id = m.tenant_id and e.site_id = m.site_id where m.tenant_id = $1 and m.resource_id = $4)
			       or r.edge_id in (select e.id from machine m join edge_node e on e.tenant_id = m.tenant_id and e.site_id = m.site_id where m.tenant_id = $1 and m.resource_id = $4))
			order by r.received_at desc limit 1`, p.TenantID, in.Node, now.Add(-maxAge), c.ResourceID).Scan(&edge, &val, &typ, &readAt, &receivedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeNoReading, "no recent reading of this node: the connector has not reported one")
		}
		if err != nil {
			return err
		}
		if err := scan(tx.QueryRow(ctx, `insert into pi_readout_claim (tenant_id, id, recipe_id, step_key, edge_id, node, value, read_at, claimed_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			on conflict (tenant_id, recipe_id, step_key) do update set edge_id = excluded.edge_id, node = excluded.node, value = excluded.value, read_at = excluded.read_at, claimed_at = excluded.claimed_at
			returning node, value, $10::text, read_at, edge_id::text, id::text, claimed_at`, p.TenantID, ids.New(), c.ID, stepKey, edge, in.Node, val, readAt, now, typ)); err != nil {
			return err
		}
		return nil
	})
	out.AgeSeconds = int(now.Sub(out.receivedAt).Seconds())
	return out, err
}

// ReadoutClaim is the reading handed out for a step.
type ReadoutClaim struct {
	ID     string
	EdgeID string
	Value  decimal.Decimal
	ReadAt time.Time
}

// PIStepData is what the service holds about a kiosk recipe when a step message arrives.
type PIStepData struct {
	Recipe    ControlRecipe
	Messages  []domain.ExecMessage
	StartedAt time.Time
	Claims    map[string]ReadoutClaim
}

// PIStepData reads a kiosk recipe, the messages already recorded for its steps, when it started and the readout claims.
func (s *Postgres) PIStepData(ctx context.Context, recipeID string) (PIStepData, error) {
	p, _ := tenant.FromContext(ctx)
	var out PIStepData
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := scanRecipe(tx.QueryRow(ctx, `select `+recipeColumns+` from control_recipe r where r.tenant_id = $1 and r.id = $2`, p.TenantID, recipeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return recipeNotFound()
		}
		if err != nil {
			return err
		}
		out.Recipe = c
		if out.Messages, err = loadExecMessages(ctx, tx, p.TenantID, c); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce((select min(at) from control_recipe_event where tenant_id = $1 and recipe_id = $2 and to_status = 'in-execution'), $3)`, p.TenantID, c.ID, c.CreatedAt).Scan(&out.StartedAt); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select step_key, id::text, edge_id::text, value, read_at from pi_readout_claim where tenant_id = $1 and recipe_id = $2`, p.TenantID, c.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Claims = map[string]ReadoutClaim{}
		for rows.Next() {
			var key string
			var cl ReadoutClaim
			if err := rows.Scan(&key, &cl.ID, &cl.EdgeID, &cl.Value, &cl.ReadAt); err != nil {
				return err
			}
			out.Claims[key] = cl
		}
		return rows.Err()
	})
	return out, err
}
