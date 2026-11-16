package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// PartnerRow is one account's balance (debit less credit, unit currency) a unit's ledger holds with one counterparty (#40).
type PartnerRow struct {
	Account string
	// CounterpartyUnitID is the group unit whose legal entity the ledger lines name as trading partner.
	CounterpartyUnitID string
	Net                decimal.Decimal
}

// UnitPartnerLedger is what one unit's ledger returned per trading partner for the period.
type UnitPartnerLedger struct {
	UnitID   string
	Currency string
	Rows     []PartnerRow
}

// PartnerLine is the intercompany part of one account of one unit with one counterparty, translated like a collected line. It is a
// memo of the collected account balance (never added to the trial balance again): it says how much of the account is with whom.
type PartnerLine struct {
	UnitID               string
	CounterpartyUnitID   string
	MainAccount          string
	ConsolidationAccount string
	Type                 string
	Role                 string
	UnitCurrency         string
	UnitAmount           decimal.Decimal
	Rate                 decimal.Decimal
	GroupAmount          decimal.Decimal
}

// isICRole reports whether a role is read per trading partner.
func isICRole(role string) bool {
	switch role {
	case RoleICSales, RoleICCost, RoleICReceivable, RoleICPayable, RoleICPurchases, RoleICCOGS, RoleICInventory:
		return true
	}
	return false
}

// HasICRoles reports whether the mapping gives any account an intercompany role, so a collection has partner balances to read.
func HasICRoles(mapping []AccountMapping) bool {
	for _, m := range mapping {
		if isICRole(m.Role) {
			return true
		}
	}
	return false
}

// CollectPartners maps each unit's partner balances to consolidation accounts and translates them into the group currency the way
// Collect does: income accounts at the average rate, balance sheet accounts at the closing rate, every line rounded once to the group
// currency's minor unit. Only accounts mapped with an intercompany role are kept, because only those are read per partner.
func CollectPartners(groupCurrency string, mapping []AccountMapping, units []UnitPartnerLedger, rates map[string]CollectRate) ([]PartnerLine, error) {
	byAccount := make(map[string]AccountMapping, len(mapping))
	for _, m := range mapping {
		byAccount[m.MainAccount] = m
	}
	places := int32(money.MinorUnits(groupCurrency))
	var out []PartnerLine
	for _, u := range units {
		var rate CollectRate
		if u.Currency == groupCurrency {
			rate = CollectRate{Average: decimal.NewFromInt(1), Closing: decimal.NewFromInt(1)}
		} else {
			r, ok := rates[u.Currency]
			if !ok || !r.Average.IsPositive() || !r.Closing.IsPositive() {
				return nil, &Error{Code: CodeCollectionError, Detail: fmt.Sprintf("unit %s is in %s and needs a positive average and closing rate to %s", u.UnitID, u.Currency, groupCurrency)}
			}
			rate = r
		}
		for _, row := range u.Rows {
			m, ok := byAccount[row.Account]
			if !ok || !isICRole(m.Role) {
				continue
			}
			l := PartnerLine{UnitID: u.UnitID, CounterpartyUnitID: row.CounterpartyUnitID, MainAccount: row.Account, ConsolidationAccount: m.ConsolidationAccount,
				Type: m.Type, Role: m.Role, UnitCurrency: u.Currency, UnitAmount: row.Net, Rate: rate.Closing}
			if m.Type == AccountIncome {
				l.Rate = rate.Average
			}
			l.GroupAmount = row.Net.Mul(l.Rate).Round(places)
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.UnitID != b.UnitID {
			return a.UnitID < b.UnitID
		}
		if a.MainAccount != b.MainAccount {
			return a.MainAccount < b.MainAccount
		}
		return a.CounterpartyUnitID < b.CounterpartyUnitID
	})
	return out, nil
}

// pairFlows are the intercompany amounts of one seller and buyer, summed from the partner lines. Debit balances are positive.
type pairFlows struct {
	found bool
	// Seller side.
	salesUnit, salesGroup           decimal.Decimal // credit balance of ic_sales, as a positive amount
	costUnit, costGroup             decimal.Decimal
	receivableUnit, receivableGroup decimal.Decimal
	// Buyer side.
	payableUnit, payableGroup decimal.Decimal // credit balance of ic_payable, as a positive amount
	purchasesGroup, cogsGroup decimal.Decimal
	inventoryGroup            decimal.Decimal
	hasInventory              bool
}

// flowsBetween sums the partner lines of the sellers about the buyers and of the buyers about the sellers (sets of unit ids).
func flowsBetween(lines []PartnerLine, sellers, buyers map[string]bool) pairFlows {
	var f pairFlows
	for _, l := range lines {
		switch {
		case sellers[l.UnitID] && buyers[l.CounterpartyUnitID]:
			switch l.Role {
			case RoleICSales:
				f.salesUnit, f.salesGroup, f.found = f.salesUnit.Sub(l.UnitAmount), f.salesGroup.Sub(l.GroupAmount), true
			case RoleICCost:
				f.costUnit, f.costGroup, f.found = f.costUnit.Add(l.UnitAmount), f.costGroup.Add(l.GroupAmount), true
			case RoleICReceivable:
				f.receivableUnit, f.receivableGroup, f.found = f.receivableUnit.Add(l.UnitAmount), f.receivableGroup.Add(l.GroupAmount), true
			}
		case buyers[l.UnitID] && sellers[l.CounterpartyUnitID]:
			switch l.Role {
			case RoleICPayable:
				f.payableUnit, f.payableGroup, f.found = f.payableUnit.Sub(l.UnitAmount), f.payableGroup.Sub(l.GroupAmount), true
			case RoleICPurchases:
				f.purchasesGroup, f.found = f.purchasesGroup.Add(l.GroupAmount), true
			case RoleICCOGS:
				f.cogsGroup, f.found = f.cogsGroup.Add(l.GroupAmount), true
			case RoleICInventory:
				f.inventoryGroup, f.hasInventory, f.found = f.inventoryGroup.Add(l.GroupAmount), true, true
			}
		}
	}
	return f
}

// UnrealizedFromPartners estimates the unrealised profit in the buyer's closing inventory (IFRS 10 B86(c), IAS 28.28) when neither
// receipt layers nor a stated unsold share exist. The closing intercompany inventory at the buyer's cost (which is the seller's price)
// is the balance of the buyer's ic_inventory accounts with the seller when it has them, else the goods it bought from the seller in the
// fiscal year less the cost of those it sold on (ic_purchases less ic_cogs). It is capped at what the seller sold the buyer. The profit
// is that inventory times the seller's margin on its sales to this buyer, (sales less cost) / sales. It is an estimate: goods bought in
// earlier years and still held are missed without ic_inventory, and the buyer's cost of goods sold is taken to be intercompany goods at
// the seller's price.
func UnrealizedFromPartners(f pairFlows, groupCurrency string) (profit, inventory, marginPct decimal.Decimal) {
	if !f.salesGroup.IsPositive() {
		return decimal.Zero, decimal.Zero, decimal.Zero
	}
	inventory = f.purchasesGroup.Sub(f.cogsGroup)
	if f.hasInventory {
		inventory = f.inventoryGroup
	}
	if inventory.IsNegative() {
		inventory = decimal.Zero
	}
	if inventory.GreaterThan(f.salesGroup) {
		inventory = f.salesGroup
	}
	margin := f.salesGroup.Sub(f.costGroup).Div(f.salesGroup)
	if margin.IsNegative() {
		margin = decimal.Zero
	}
	return round(inventory.Mul(margin), groupCurrency), inventory, margin.Mul(decimal.NewFromInt(100)).Round(4)
}

// profitEstimate is the unrealised profit RunUnits estimates from the partner balances of one pair.
type profitEstimate struct {
	Profit, Inventory, MarginPct decimal.Decimal
}

// applyPartnerFlows fills the pair of subsidiary u from the partner lines between u and its holders (#40). It returns false when the
// partner lines say nothing about the pair, so the pair keeps what the unit's own account balances gave. The seller is the subsidiary
// when the unit's terms say so, otherwise its holders (a parent selling to several subsidiaries has one pair per subsidiary, each with
// its own sales, receivable, payable and margin, so each subsidiary's unrealised profit is eliminated on its own). The estimate of the
// unrealised profit is returned for the caller to use only when no layers and no stated unsold share exist.
func applyPartnerFlows(pair *Pair, in DeriveInput, u Unit, terms UnitTerms, end time.Time) (bool, profitEstimate) {
	holders := map[string]bool{}
	for _, h := range u.Holdings {
		if inForce(OwnershipInterval{From: h.From, To: h.To, Percent: h.Percent}, end) {
			holders[h.HolderUnitID] = true
		}
	}
	self := map[string]bool{u.ID: true}
	sellers, buyers := holders, self
	if terms.SellerIsSubsidiary {
		sellers, buyers = self, holders
	}
	f := flowsBetween(in.PartnerLines, sellers, buyers)
	if !f.found {
		return false, profitEstimate{}
	}
	if terms.SellerIsSubsidiary {
		// The subsidiary sells: its own currency is the seller's, the parent's payable is in the group currency as collected.
		pair.ICSales, pair.ICCost, pair.ICReceivable = f.salesUnit, f.costUnit, f.receivableUnit
		pair.PayableGroup = f.payableGroup
	} else {
		// The parent sells: its sales, cost and receivable are already in the group currency, the subsidiary's payable is not.
		pair.ICSalesGroup, pair.ICCostGroup, pair.ReceivableGroup = decPtr(f.salesGroup), decPtr(f.costGroup), decPtr(f.receivableGroup)
		pair.PayableUnit = decPtr(f.payableUnit)
		pair.ICSales, pair.ICCost, pair.ICReceivable, pair.PayableGroup = decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	}
	profit, inventory, margin := UnrealizedFromPartners(f, in.GroupCurrency)
	return true, profitEstimate{Profit: profit, Inventory: inventory, MarginPct: margin}
}

func decPtr(d decimal.Decimal) *decimal.Decimal { return &d }

// outsidePairWarnings names intercompany balances between two units that are not a holder and its subsidiary (a subsidiary selling to a
// sibling): the run eliminates pairs of a holder and its subsidiary only, so these stay in the consolidated figures.
func outsidePairWarnings(in DeriveInput, covered map[[2]string]bool) []string {
	seen := map[[2]string]bool{}
	var pairs [][2]string
	for _, l := range in.PartnerLines {
		if l.GroupAmount.IsZero() || l.CounterpartyUnitID == "" {
			continue
		}
		a, b := l.UnitID, l.CounterpartyUnitID
		if a > b {
			a, b = b, a
		}
		k := [2]string{a, b}
		if covered[k] || seen[k] {
			continue
		}
		seen[k] = true
		pairs = append(pairs, k)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	var out []string
	for _, k := range pairs {
		out = append(out, (&Error{Code: CodeICOutsidePairs, Detail: fmt.Sprintf("intercompany balances between units %s and %s are not between a holder and its subsidiary, so no elimination covers them", k[0], k[1])}).Error())
	}
	return out
}
