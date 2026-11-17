package dq

import (
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/tools/migration-factory/internal/domain"
)

// The extract rows of each pack as its suite reads them: one column per field, decimals as their decimal strings. The load
// path and the parity tests build the rows the same way, so the suite judges exactly what the Go validator judged.

// OnhandRows returns the onhand lines as suite rows.
func OnhandRows(in []domain.OnhandLine) []Row {
	out := make([]Row, len(in))
	for i, l := range in {
		out[i] = Row{"legacyId": l.LegacyID, "item": l.Item, "site": l.Site, "warehouse": l.Warehouse, "location": l.Location, "batch": l.Batch,
			"serial": l.Serial, "unit": l.Unit, "currency": l.Currency, "quantity": l.Quantity.String(), "value": l.Value.String()}
	}
	return out
}

// BankRows returns the bank balance lines as suite rows.
func BankRows(in []domain.BankBalanceLine) []Row {
	out := make([]Row, len(in))
	for i, l := range in {
		out[i] = Row{"legacyId": l.LegacyID, "bankAccount": l.BankAccount, "currency": l.Currency, "balance": l.Balance.String()}
	}
	return out
}

// GLRows returns the gl balances as suite rows.
func GLRows(in []domain.GLBalance) []Row {
	out := make([]Row, len(in))
	for i, l := range in {
		out[i] = Row{"legacyId": l.LegacyID, "account": l.Account, "dimensions": l.Dimensions, "currency": l.Currency, "balance": l.Balance.String()}
	}
	return out
}

// OpenItemRows returns the open items as suite rows; side is AR or AP (a blank ledger is AR).
func OpenItemRows(in []domain.Record) []Row {
	out := make([]Row, len(in))
	for i, r := range in {
		out[i] = Row{"legacyId": r.LegacyID, "side": r.Side(), "party": r.Party, "account": r.Account, "currency": r.Currency, "amount": r.Amount.String()}
	}
	return out
}

// OpenOrderRows returns the open order lines as suite rows.
func OpenOrderRows(in []domain.OpenOrderLine) []Row {
	out := make([]Row, len(in))
	for i, o := range in {
		out[i] = Row{"legacyId": o.LegacyID, "orderType": o.OrderType, "orderNumber": o.OrderNumber, "lineNumber": o.LineNumber, "item": o.Item,
			"party": o.Party, "unit": o.Unit, "currency": o.Currency, "orderedQty": o.OrderedQty.String(), "deliveredQty": o.DeliveredQty.String(),
			"unitPrice": o.UnitPrice.String(), "deliveryDate": o.DeliveryDate}
	}
	return out
}

// WIPProdRows returns the production WIP lines as suite rows.
func WIPProdRows(in []domain.WIPProdLine) []Row {
	out := make([]Row, len(in))
	for i, w := range in {
		out[i] = Row{"legacyId": w.LegacyID, "productionOrder": w.ProductionOrder, "item": w.Item, "currency": w.Currency,
			"material": w.Material.String(), "labor": w.Labor.String(), "overhead": w.Overhead.String()}
	}
	return out
}

// PayrollYTDRows returns the payroll year-to-date lines as suite rows.
func PayrollYTDRows(in []domain.PayrollYTDLine) []Row {
	out := make([]Row, len(in))
	for i, p := range in {
		out[i] = Row{"legacyId": p.LegacyID, "worker": p.Worker, "taxYear": p.TaxYear, "currency": p.Currency,
			"gross": p.Gross.String(), "tax": p.Tax.String(), "deductions": p.Deductions.String(), "net": p.Net.String()}
	}
	return out
}

// FixedAssetRows returns the asset books as suite rows. Two columns are derived from the line, as the Go validator derives them:
// nbv (acquisition cost less accumulated depreciation) and nextDepreciationDifference, the distance between the legacy
// projection of the next period's depreciation and the one recomputed from the loaded values (BR-49.6); it is 0 for a line whose
// method or life makes no projection possible, which the other checks flag.
func FixedAssetRows(in []domain.FixedAssetLine) []Row {
	out := make([]Row, len(in))
	for i, a := range in {
		diff := decimal.Zero
		if (a.Method == domain.DepStraightLine || a.Method == domain.DepDecliningBalance) && a.UsefulLifeMonths > 0 {
			diff = a.ProjectNextDepreciation().Sub(a.NextDepreciation).Abs()
		}
		out[i] = Row{"legacyId": a.LegacyID, "asset": a.Asset, "book": a.Book, "currency": a.Currency, "method": a.Method,
			"usefulLifeMonths": strconv.Itoa(a.UsefulLifeMonths), "remainingLifeMonths": strconv.Itoa(a.RemainingLifeMonths),
			"acquisitionCost": a.AcquisitionCost.String(), "accumulatedDepreciation": a.AccumulatedDepreciation.String(), "salvageValue": a.SalvageValue.String(),
			"nextDepreciation": a.NextDepreciation.String(), "nbv": a.NBV().String(), "nextDepreciationDifference": diff.String()}
	}
	return out
}

// ProjectWIPRows returns the project opening lines as suite rows. Two columns are derived from the line against the extract's
// cutoff: daysAfterCutoff (whole days the transaction date is after the cutoff date, zero or less when it is not, 0 for a missing
// date, which the date check flags) and wipSalesOfNonChargeable (the WIP sales value of a non-chargeable line, else 0).
func ProjectWIPRows(in []domain.OpeningLine, cutoff time.Time) []Row {
	out := make([]Row, len(in))
	cutDay := cutoff.UTC().Truncate(24 * time.Hour)
	for i, l := range in {
		date, days := "", 0
		if !l.TransactionDate.IsZero() {
			date = l.TransactionDate.Format(time.DateOnly)
			days = int(l.TransactionDate.UTC().Truncate(24*time.Hour).Sub(cutDay).Hours() / 24)
			if l.TransactionDate.After(cutoff) && days <= 0 {
				days = 1
			}
		}
		nonChargeable := decimal.Zero
		if l.BillingStatus == domain.BillingNonChargeable {
			nonChargeable = l.WIPSales
		}
		out[i] = Row{"legacyId": l.LegacyID, "projectId": l.ProjectID, "category": string(l.Category), "billingStatus": string(l.BillingStatus),
			"currency": l.Currency, "transactionDate": date, "daysAfterCutoff": strconv.Itoa(days), "wipSalesOfNonChargeable": nonChargeable.String()}
	}
	return out
}

// ISO4217 is the currency reference set of the suites: the currencies the packs accept.
func ISO4217() map[string]bool {
	out := map[string]bool{}
	for _, c := range domain.Currencies() {
		out[c] = true
	}
	return out
}

// SetOf turns a master list into a reference set.
func SetOf[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
