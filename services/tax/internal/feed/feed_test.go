package feed_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/feed"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
	"github.com/BenjaminSRussell/passage/services/tax/migrations"
)

const pump = "0190f0a0-0000-7000-8000-0000000000a1"

type fixture struct {
	s        *store.Store
	app      *pgxpool.Pool
	tenantID string
	entity   string
	ctx      context.Context
	handlers map[string]eventing.Handler
	inbox    *eventing.PgInbox
}

func setup(t *testing.T, home string) *fixture {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	f := &fixture{s: store.New(app), app: app, entity: ids.New()}
	user := testkit.Ctx(ids.New(), "tax.intrastat.manage")
	p, _ := tenant.FromContext(user)
	p.LegalEntityID = f.entity
	f.tenantID = p.TenantID
	f.ctx = tenant.With(user, p)
	if home != "" {
		if err := f.s.PutEntitySetting(f.ctx, store.EntitySetting{HomeCountry: home, VATID: home + "123456789"}); err != nil {
			t.Fatal(err)
		}
	}
	f.handlers, f.inbox = feed.Handlers(f.s), eventing.NewPgInbox(app)
	return f
}

// deliver hands an event to its handler the way the consumer does (not Idempotent): inside the inbox transaction, as the consumer's
// service principal. It reports whether the event was applied or a duplicate.
func (f *fixture) deliver(t *testing.T, id, typ, source string, data any) (bool, error) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	evt := events.Event{ID: id, Type: typ, Source: source, TenantID: f.tenantID, Data: raw}
	actor, err := tenant.ServicePrincipal(feed.ConsumerName, f.tenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), actor)
	return f.inbox.Once(ctx, feed.ConsumerName, evt.ID, func(tx pgx.Tx) error { return f.handlers[typ].Handle(ctx, tx, evt) })
}

func (f *fixture) movements(t *testing.T, status string) []domain.Movement {
	t.Helper()
	out, err := f.s.ListMovements(f.ctx, status)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fixture) slip(number, partner string, qty, cost string, extra map[string]any) map[string]any {
	line := map[string]any{"product_id": pump, "qty": qty, "cost_amount": cost, "order_line_id": ids.New(), "cost_snapshot_id": "s1"}
	for k, v := range extra {
		line[k] = v
	}
	d := map[string]any{"packing_slip_id": ids.New(), "packing_slip_number": number, "legal_entity_id": f.entity, "posting_date": "2026-03-04", "currency": "EUR", "lines": []any{line}}
	if partner != "" {
		d["partner_country"] = partner
	}
	return d
}

func isPermanent(err error) bool { return err != nil && eventing.IsPermanent(err) }

// Worked example (#13): the declarant is German. Packing slip PS-0000007 of 4 pumps at 500.00 cost each, shipped to a customer
// VAT registered in France: the event carries 84137021, origin Italy, 50 kg net and statistical value 2000.00. One dispatch is staged
// with that data on its line, and transferring it into a draft journal needs no product mapping: net mass 50, origin IT, value 2000.00.
func TestFeed_PackingSlipToAnEUPartnerIsStagedWithTheLineData(t *testing.T) {
	f := setup(t, "DE")
	data := f.slip("PS-0000007", "FR", "4", "2000.00", map[string]any{"commodity_code": "84137021", "origin_country": "IT", "net_weight": "50", "statistical_value": "2000.00"})
	if applied, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", data); err != nil || !applied {
		t.Fatalf("deliver: %v %v", applied, err)
	}
	staged := f.movements(t, "staged")
	if len(staged) != 1 {
		t.Fatalf("staged %+v", staged)
	}
	m := staged[0]
	if m.Flow != domain.FlowDispatch || m.SourceRef != "FUL:PS-0000007" || m.PartnerCountry != "FR" || m.MovementDate.Format("2006-01-02") != "2026-03-04" || len(m.Lines) != 1 {
		t.Fatalf("movement %+v", m)
	}
	l := m.Lines[0]
	if l.ProductID != pump || l.Qty.String() != "4" || l.Value.String() != "2000" || l.CommodityCode != "84137021" || l.OriginCountry != "IT" || l.NetMass == nil || l.NetMass.String() != "50" {
		t.Fatalf("line %+v", l)
	}
	j, err := f.s.CreateJournal(f.ctx, "dispatch", domain.Day("2026-03-01"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.s.TransferMovements(f.ctx, j.ID, domain.MovementDefaults{TransactionCode: "11", TransportMode: "3"})
	if err != nil || res.Added != 1 || res.Complete != 1 || len(res.Unmapped) != 0 {
		t.Fatalf("transfer %+v %v", res, err)
	}
	got, _ := f.s.GetJournal(f.ctx, j.ID)
	if len(got.Lines) != 1 || got.Lines[0].CommodityCode != "84137021" || got.Lines[0].OriginCountry != "IT" || got.Lines[0].NetMass.String() != "50" ||
		got.Lines[0].StatisticalValue.String() != "2000" || got.Lines[0].PartnerCountry != "FR" {
		t.Fatalf("journal lines %+v", got.Lines)
	}
}

// A redelivery (the same event id) is a duplicate through consumer_inbox and stages nothing; the same slip under another event id is
// one movement still (its source reference is unique), and the same slip with other content is refused.
func TestFeed_RedeliveryAndReplayStageOnce(t *testing.T) {
	f := setup(t, "DE")
	data := f.slip("PS-0000008", "FR", "2", "100.00", map[string]any{"commodity_code": "84137021", "origin_country": "IT", "net_weight": "25"})
	id := ids.New()
	if applied, err := f.deliver(t, id, feed.PackingSlipPostedType, "fulfillment", data); err != nil || !applied {
		t.Fatal(applied, err)
	}
	if applied, err := f.deliver(t, id, feed.PackingSlipPostedType, "fulfillment", data); err != nil || applied {
		t.Fatalf("the same event id again: applied %v err %v, want a duplicate", applied, err)
	}
	if applied, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", data); err != nil || !applied {
		t.Fatalf("a new event id, same slip: %v %v", applied, err)
	}
	if n := len(f.movements(t, "staged")); n != 1 {
		t.Fatalf("staged %d movements, want 1", n)
	}
	changed := f.slip("PS-0000008", "FR", "3", "150.00", map[string]any{"commodity_code": "84137021", "origin_country": "IT", "net_weight": "37.5"})
	if _, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", changed); !isPermanent(err) {
		// The domain error of staging other content under a staged reference parks the event.
		t.Fatalf("the same slip with other content: %v", err)
	}
	// The inbox recorded the first two events (the refused one rolled back with its transaction).
	var seen int
	if err := dbx.WithTenantTx(f.ctx, f.app, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select count(*) from consumer_inbox where consumer = $1`, feed.ConsumerName).Scan(&seen)
	}); err != nil || seen != 2 {
		t.Errorf("consumer_inbox holds %d events (%v), want 2", seen, err)
	}
}

// Only EU cross-border movements are staged: the partner's VAT country must be an EU member state other than the declarant's own. A
// domestic partner, a non-EU partner and an event with no partner country stage nothing (and are acknowledged, not parked).
func TestFeed_OnlyEUCrossBorderMovementsAreStaged(t *testing.T) {
	f := setup(t, "DE")
	for i, tc := range []struct {
		name, partner string
		staged        bool
	}{{"France", "FR", true}, {"domestic", "DE", false}, {"United States", "US", false}, {"United Kingdom", "GB", false}, {"unknown partner", "", false}} {
		data := f.slip("PS-X"+string(rune('A'+i)), tc.partner, "1", "10.00", nil)
		if applied, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", data); err != nil || !applied {
			t.Fatalf("%s: %v %v", tc.name, applied, err)
		}
	}
	staged := f.movements(t, "staged")
	if len(staged) != 1 || staged[0].PartnerCountry != "FR" {
		t.Fatalf("staged %+v, want only the French dispatch", staged)
	}
	for _, c := range []struct {
		home, partner string
		want          bool
	}{{"DE", "FR", true}, {"FR", "DE", true}, {"DE", "DE", false}, {"DE", "US", false}, {"GB", "FR", false}, {"DE", "", false}, {"IE", "SE", true}, {"DE", "EL", true}, {"DE", "XI", true}, {"DE", "GR", true}} {
		if got := feed.CrossBorder(c.home, c.partner); got != c.want {
			t.Errorf("CrossBorder(%q, %q) = %v", c.home, c.partner, got)
		}
	}
}

// receipt.posted maps to an arrival from the vendor's country; the line value is amount_acct without a statistical value, and a line
// with no customs data stays an unmapped product until the product is mapped.
func TestFeed_ReceiptIsAnArrival(t *testing.T) {
	f := setup(t, "DE")
	data := map[string]any{"receipt_id": ids.New(), "receipt_number": "RCV-00000042", "legal_entity_id": f.entity, "vendor_id": ids.New(), "partner_country": "NL",
		"posting_date": "2026-03-05", "currency": "EUR", "lines": []any{
			map[string]any{"line_no": 1, "product_id": pump, "qty": "9", "amount_acct": "900.0000"},
			map[string]any{"line_no": 2, "product_id": pump, "qty": "0", "amount_acct": "0"},
		}}
	if applied, err := f.deliver(t, ids.New(), feed.ReceiptPostedType, "/passage/receiving/receiving", data); err != nil || !applied {
		t.Fatalf("%v %v", applied, err)
	}
	staged := f.movements(t, "staged")
	if len(staged) != 1 || staged[0].Flow != domain.FlowArrival || staged[0].SourceRef != "RCV:RCV-00000042" || staged[0].PartnerCountry != "NL" ||
		len(staged[0].Lines) != 1 || staged[0].Lines[0].Value.String() != "900" || staged[0].Lines[0].CommodityCode != "" {
		t.Fatalf("staged %+v", staged)
	}
	j, _ := f.s.CreateJournal(f.ctx, "arrival", domain.Day("2026-03-01"))
	res, err := f.s.TransferMovements(f.ctx, j.ID, domain.MovementDefaults{})
	if err != nil || res.Added != 0 || len(res.Unmapped) != 1 {
		t.Fatalf("a line with no customs data and no mapping stays staged: %+v %v", res, err)
	}
}

func TestFeed_RefusalsAreParked(t *testing.T) {
	f := setup(t, "DE")
	good := f.slip("PS-0000009", "FR", "1", "10.00", nil)
	if _, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "billing", good); !isPermanent(err) {
		t.Errorf("another source: %v", err)
	}
	noEntity := f.slip("PS-0000010", "FR", "1", "10.00", nil)
	noEntity["legal_entity_id"] = ""
	if _, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", noEntity); !isPermanent(err) {
		t.Errorf("no legal entity: %v", err)
	}
	badDate := f.slip("PS-0000011", "FR", "1", "10.00", nil)
	badDate["posting_date"] = "soon"
	if _, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", badDate); !isPermanent(err) {
		t.Errorf("bad date: %v", err)
	}
	noNumber := f.slip("", "FR", "1", "10.00", nil)
	if _, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", noNumber); !isPermanent(err) {
		t.Errorf("no number: %v", err)
	}
	badCode := f.slip("PS-0000012", "FR", "1", "10.00", map[string]any{"commodity_code": "12"})
	if _, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", badCode); !isPermanent(err) {
		t.Errorf("bad commodity code: %v", err)
	}
	if n := len(f.movements(t, "staged")); n != 0 {
		t.Errorf("%d movements staged by refused events", n)
	}
	// A legal entity with no Intrastat setting cannot classify the movement.
	bare := setup(t, "")
	if _, err := bare.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", bare.slip("PS-1", "FR", "1", "1", nil)); !isPermanent(err) {
		t.Errorf("no entity setting: %v", err)
	}
}

// Reviewer examples (#13): the partner country is where the goods went, not the customer's VAT country. A DE declarant shipping to the
// Austrian warehouse of a DE-registered customer ships to AT (event partner AT): staged. A FR-registered customer collecting in Germany
// is a domestic delivery (event partner DE): not staged.
func TestFeed_PartnerCountryIsTheShipToNotTheVATCountry(t *testing.T) {
	f := setup(t, "DE")
	if applied, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", f.slip("PS-AT", "AT", "1", "10.00", map[string]any{"commodity_code": "84137021"})); err != nil || !applied {
		t.Fatal(applied, err)
	}
	if applied, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", f.slip("PS-DE", "DE", "1", "10.00", map[string]any{"commodity_code": "84137021"})); err != nil || !applied {
		t.Fatal(applied, err)
	}
	staged := f.movements(t, "staged")
	if len(staged) != 1 || staged[0].SourceRef != "FUL:PS-AT" || staged[0].PartnerCountry != "AT" {
		t.Fatalf("staged %+v, want only the dispatch to Austria", staged)
	}
	if applied, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", f.slip("PS-EL", "EL", "1", "10.00", nil)); err != nil || !applied {
		t.Fatal(applied, err)
	}
	if got := f.movements(t, "staged"); len(got) != 2 || got[0].PartnerCountry+got[1].PartnerCountry != "ATGR" && got[0].PartnerCountry+got[1].PartnerCountry != "GRAT" {
		t.Fatalf("EL is read as GR: %+v", got)
	}
}

// An event posted while the party or product read failed is parked, not acknowledged as a partner without a registration.
func TestFeed_LookupFailedEventsAreParked(t *testing.T) {
	f := setup(t, "DE")
	slip := f.slip("PS-F1", "AT", "1", "10.00", nil)
	slip["customs_lookup_failed"] = true
	if _, err := f.deliver(t, ids.New(), feed.PackingSlipPostedType, "fulfillment", slip); !isPermanent(err) {
		t.Fatalf("slip: %v", err)
	}
	receipt := map[string]any{"receipt_number": "RCV-F1", "legal_entity_id": f.entity, "partner_country": "AT", "posting_date": "2026-03-05", "customs_lookup_failed": true,
		"lines": []any{map[string]any{"product_id": pump, "qty": "1", "amount_acct": "1"}}}
	if _, err := f.deliver(t, ids.New(), feed.ReceiptPostedType, "/passage/receiving/receiving", receipt); !isPermanent(err) {
		t.Fatalf("receipt: %v", err)
	}
	if n := len(f.movements(t, "staged")); n != 0 {
		t.Errorf("%d movements staged", n)
	}
}
