package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
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

// NodeReading is the latest value known for a node.
type NodeReading struct {
	Node       string          `json:"node"`
	Value      decimal.Decimal `json:"value"`
	NodeType   string          `json:"nodeType"`
	ReadAt     time.Time       `json:"readAt"`
	AgeSeconds int             `json:"ageSeconds"`
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

// LatestNodeReading returns the freshest reading of the node no older than maxAge, or SF-0196.
func (s *Postgres) LatestNodeReading(ctx context.Context, node string, maxAge time.Duration, now time.Time) (NodeReading, error) {
	p, _ := tenant.FromContext(ctx)
	var out NodeReading
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select node, value, node_type, read_at from node_reading where tenant_id = $1 and node = $2 and read_at >= $3 order by read_at desc limit 1`,
			p.TenantID, node, now.Add(-maxAge)).Scan(&out.Node, &out.Value, &out.NodeType, &out.ReadAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeNoReading, "no recent reading of this node: the connector has not reported one")
		}
		return err
	})
	out.AgeSeconds = int(now.Sub(out.ReadAt).Seconds())
	return out, err
}
