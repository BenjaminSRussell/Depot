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

// pairFlows are the intercompany amounts of one seller and buyer, resolved from the partner lines and the unit's role-account totals.
// Debit balances are positive; credit balances (sales, payables) are stored as positive amounts.
type pairFlows struct {
	// Seller side.
	salesUnit, salesGroup           decimal.Decimal
	costUnit, costGroup             decimal.Decimal
	receivableUnit, receivableGroup decimal.Decimal
	// Buyer side.
	payableUnit, payableGroup decimal.Decimal
	purchasesGroup, cogsGroup decimal.Decimal
	inventoryGroup            decimal.Decimal
	hasInventory              bool
	// costKnown is false when neither a tagged ic_cost nor an ic_cost account total says what the seller's goods cost, so no margin
	// can be worked out (a missing cost is never read as zero, which would make the margin 100 percent).
	costKnown bool
}

// partnerResolution is what applyPartnerFlows found for one pair.
type partnerResolution struct {
	Applied  bool
	Estimate profitEstimate
	// EstimateOK is false when the unrealised profit cannot be estimated (no seller cost, no seller sales).
	EstimateOK bool
	CostKnown  bool
	// Flows are the resolved amounts, so an override of the request can be applied before the estimate is made.
	Flows    pairFlows
	Warnings []string
}

type amt struct {
	unit, group decimal.Decimal
	n           int
}

func sumPartner(lines []PartnerLine, owners, counter map[string]bool, role string) amt {
	var a amt
	for _, l := range lines {
		if l.Role == role && owners[l.UnitID] && (counter == nil || counter[l.CounterpartyUnitID]) {
			a.unit, a.group, a.n = a.unit.Add(l.UnitAmount), a.group.Add(l.GroupAmount), a.n+1
		}
	}
	return a
}

func sumRole(lines []CollectLine, owners map[string]bool, role string) amt {
	var a amt
	for _, l := range lines {
		if l.Role == role && owners[l.UnitID] {
			a.unit, a.group, a.n = a.unit.Add(l.UnitAmount), a.group.Add(l.GroupAmount), a.n+1
		}
	}
	return a
}

// UnrealizedFromPartners estimates the unrealised profit in the buyer's closing inventory (IFRS 10 B86(c), IAS 28.28) when neither
// receipt layers nor a stated unsold share exist. The closing intercompany inventory at the buyer's cost (which is the seller's price)
// is the balance of the buyer's ic_inventory accounts with the seller when it has them, else the goods it bought from the seller in the
// fiscal year less the cost of those it sold on (ic_purchases less ic_cogs). It is capped at what the seller sold the buyer. The profit
// is that inventory times the seller's margin on its sales to this buyer, (sales less cost) / sales. ok is false when the seller's cost
// is unknown (it is never read as zero) or the seller sold nothing. It is an estimate: goods bought in earlier years and still held are
// missed without ic_inventory, and the buyer's cost of goods sold is taken to be intercompany goods at the seller's price.
func UnrealizedFromPartners(f pairFlows, groupCurrency string) (profit, inventory, marginPct decimal.Decimal, ok bool) {
	if !f.salesGroup.IsPositive() || !f.costKnown {
		return decimal.Zero, decimal.Zero, decimal.Zero, false
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
	return round(inventory.Mul(margin), groupCurrency), inventory, margin.Mul(decimal.NewFromInt(100)).Round(4), true
}

// profitEstimate is the unrealised profit RunUnits estimates from the partner balances of one pair.
type profitEstimate struct {
	Profit, Inventory, MarginPct decimal.Decimal
}

// applyPartnerFlows fills the pair of subsidiary u from the partner lines between u and its holders (#40). The seller is the subsidiary
// when the unit's terms say so, otherwise its holders (a parent selling to several subsidiaries has one pair per subsidiary, each with
// its own sales, receivable, payable and margin).
//
// Tagged lines are not trusted to be complete: payments, settlements, manual and batch journals and postings from before the rollout
// carry no partner. For every role the tagged total of the owner (all counterparties) is compared with the owner's collected account
// total. When they agree the pair's tagged figure is used. When they differ, a subsidiary's own account total is used (its account
// holds only its group counterparties' balances in practice) and for a holder's account the total is used when every tagged line is
// this pair's, else the tagged figure stands and the untagged remainder is reported; every case answers CNS-0935. A seller's cost that
// no line says is taken from the seller's ic_cost account total (shared out by sales for a holder) and, with none, the margin is
// unknown and nothing is estimated (CNS-0933). The reverse direction (the buyer's receivable from the seller and the seller's payable
// to the buyer, an intercompany loan or a charge the other way) is eliminated in E2 as well.
func applyPartnerFlows(pair *Pair, in DeriveInput, u Unit, terms UnitTerms, end time.Time) partnerResolution {
	holders := map[string]bool{}
	for _, h := range u.Holdings {
		if inForce(OwnershipInterval{From: h.From, To: h.To, Percent: h.Percent}, end) {
			holders[h.HolderUnitID] = true
		}
	}
	self := map[string]bool{u.ID: true}
	sellers, buyers := holders, self
	sellerIsUnit := false
	if terms.SellerIsSubsidiary {
		sellers, buyers = self, holders
		sellerIsUnit = true
	}
	var res partnerResolution
	// The pair is read from the lines when a line names a partner between the holder and the unit, or when the holder has tagged lines and
	// the unit has intercompany balances of its own that the untagged rest of the holder's accounts could belong to. A subsidiary with
	// neither is left alone (no intercompany balance, nothing to eliminate).
	direct, holderTagged, ownIC := false, false, false
	for _, l := range in.PartnerLines {
		direct = direct || (holders[l.UnitID] && l.CounterpartyUnitID == u.ID) || (l.UnitID == u.ID && holders[l.CounterpartyUnitID])
		holderTagged = holderTagged || holders[l.UnitID] || l.UnitID == u.ID
	}
	for _, l := range in.Lines {
		ownIC = ownIC || (l.UnitID == u.ID && isICRole(l.Role) && (!l.GroupAmount.IsZero() || !l.UnitAmount.IsZero()))
	}
	if !direct && !(holderTagged && ownIC) {
		return res // no line names a partner of this pair: the unit's own account balances stand
	}
	warn := func(code, detail string) {
		res.Warnings = append(res.Warnings, (&Error{Code: code, Detail: "unit " + u.ID + ": " + detail}).Error())
	}
	places := int32(2)
	// The subsidiaries of the holders (u among them): a holder's untagged balances cannot be told apart between them.
	var subs []string
	for _, x := range in.Units {
		for _, h := range x.Holdings {
			if holders[h.HolderUnitID] && inForce(OwnershipInterval{From: h.From, To: h.To, Percent: h.Percent}, end) {
				subs = append(subs, x.ID)
				break
			}
		}
	}
	sub := func(a, b amt) amt { return amt{unit: a.unit.Sub(b.unit), group: a.group.Sub(b.group), n: a.n} }
	add := func(a, b amt) amt { return amt{unit: a.unit.Add(b.unit), group: a.group.Add(b.group), n: a.n + b.n} }
	// counterBase names the role of the unit's own accounts that holds the other side of a holder's role.
	counterBase := map[string]string{RoleICSales: RoleICPurchases, RoleICCost: RoleICPurchases, RoleICReceivable: RoleICPayable,
		RoleICPurchases: RoleICSales, RoleICCOGS: RoleICSales, RoleICInventory: RoleICSales, RoleICPayable: RoleICReceivable}
	gateBase := map[string]string{RoleICCost: RoleICSales, RoleICCOGS: RoleICPurchases, RoleICInventory: RoleICPurchases}
	// counterMatches reports whether the unit's own counter-side accounts hold an untagged balance of the size of the holder's untagged
	// remainder (within 1 percent, which covers the translation of a foreign unit): only then can the remainder be this unit's.
	counterMatchesFor := func(role string, owners map[string]bool, unit string) bool {
		if b, ok := gateBase[role]; ok {
			role = b
		}
		mine := map[string]bool{unit: true}
		holderRem := sub(sumRole(in.Lines, owners, role), sumPartner(in.PartnerLines, owners, nil, role))
		counter := counterBase[role]
		unitRem := sub(sumRole(in.Lines, mine, counter), sumPartner(in.PartnerLines, mine, nil, counter))
		a, b := holderRem.group.Abs(), unitRem.group.Abs()
		if a.IsZero() || b.IsZero() {
			return false
		}
		tol := decimal.Max(decimal.New(1, -2), a.Mul(decimal.New(1, -2)))
		return a.Sub(b).Abs().LessThanOrEqual(tol)
	}
	// counterMatches: this unit holds the other side of the remainder and no other subsidiary of the holders does.
	counterMatches := func(role string, owners map[string]bool) bool {
		if !counterMatchesFor(role, owners, u.ID) {
			return false
		}
		for _, x := range subs {
			if x != u.ID && counterMatchesFor(role, owners, x) {
				return false
			}
		}
		return true
	}
	// resolve returns the pair's amount of one role (debit positive) and whether any line or total supports it.
	resolve := func(role string, owners, counter map[string]bool, ownerIsUnit bool) (amt, bool) {
		pairT := sumPartner(in.PartnerLines, owners, counter, role)
		allT := sumPartner(in.PartnerLines, owners, nil, role)
		tot := sumRole(in.Lines, owners, role)
		tol := decimal.New(1, -2).Mul(decimal.NewFromInt(int64(tot.n + allT.n + 1)))
		nonzero := func(a amt) bool { return a.group.Abs().GreaterThan(tol) || a.unit.Abs().GreaterThan(tol) }
		rem := sub(tot, allT) // what the account holds beyond every tagged line
		if ownerIsUnit {
			// A subsidiary's account holds its group counterparties' balances. What is tagged to another counterparty is not this pair's.
			switch {
			case pairT.n == 0 && allT.n == 0:
				if tot.n > 0 && nonzero(tot) {
					warn(CodeICUntagged, fmt.Sprintf("the %s balance %s %s has no trading partner on its ledger lines; the account total is used", role, tot.group.StringFixed(places), in.GroupCurrency))
					return tot, true
				}
				return amt{}, false
			case pairT.n == 0:
				if nonzero(rem) {
					warn(CodeICUntagged, fmt.Sprintf("the %s balance %s %s beyond the lines tagged to other counterparties has no trading partner; it is taken as this pair's", role, rem.group.StringFixed(places), in.GroupCurrency))
					return rem, true
				}
				return amt{}, false
			case !nonzero(rem):
				return pairT, true
			}
			warn(CodeICUntagged, fmt.Sprintf("the tagged %s lines total %s %s but the account holds %s; the account total less what is tagged to other counterparties is used", role, allT.group.StringFixed(places), in.GroupCurrency, tot.group.StringFixed(places)))
			return add(pairT, rem), true
		}
		// A holder's account holds the balances with all its subsidiaries: the untagged rest is attributed to a pair only when that
		// subsidiary's own counter-side accounts hold an untagged balance of the same size and no other subsidiary's do.
		switch {
		case pairT.n == 0 && allT.n == 0:
			if tot.n > 0 && nonzero(tot) {
				if counterMatches(role, owners) {
					warn(CodeICUntagged, fmt.Sprintf("the %s balance %s %s of its holder has no trading partner on its ledger lines; this unit's own accounts hold the other side and no other subsidiary does, so the account total is this pair's", role, tot.group.StringFixed(places), in.GroupCurrency))
					return tot, true
				}
				warn(CodeICUntagged, fmt.Sprintf("the %s balance %s %s of its holders has no trading partner on its ledger lines and is not attributed to this pair", role, tot.group.StringFixed(places), in.GroupCurrency))
			}
			return amt{}, false
		case pairT.n == 0:
			if nonzero(rem) {
				if counterMatches(role, owners) {
					warn(CodeICUntagged, fmt.Sprintf("the %s balance %s %s of its holders beyond the tagged lines has no trading partner; this unit's own accounts hold the other side and no other subsidiary does, so it is taken as this pair's", role, rem.group.StringFixed(places), in.GroupCurrency))
					return rem, true
				}
				warn(CodeICUntagged, fmt.Sprintf("the %s balance %s %s of its holders beyond the tagged lines has no trading partner and is not attributed to this pair", role, rem.group.StringFixed(places), in.GroupCurrency))
			}
			return amt{}, false
		case !nonzero(rem):
			return pairT, true
		case counterMatches(role, owners):
			warn(CodeICUntagged, fmt.Sprintf("the tagged %s lines of its holders total %s %s but the accounts hold %s; this unit's own accounts hold the untagged remainder, so it is taken as this pair's", role, allT.group.StringFixed(places), in.GroupCurrency, tot.group.StringFixed(places)))
			return add(pairT, rem), true
		}
		warn(CodeICUntagged, fmt.Sprintf("the %s accounts of its holders hold %s %s more than the tagged lines; that remainder is not attributed to this pair", role, rem.group.StringFixed(places), in.GroupCurrency))
		return pairT, true
	}

	var f pairFlows
	sales, okSales := resolve(RoleICSales, sellers, buyers, sellerIsUnit)
	cost, okCost := resolve(RoleICCost, sellers, buyers, sellerIsUnit)
	recv, okRecv := resolve(RoleICReceivable, sellers, buyers, sellerIsUnit)
	pay, okPay := resolve(RoleICPayable, buyers, sellers, !sellerIsUnit)
	purch, okPurch := resolve(RoleICPurchases, buyers, sellers, !sellerIsUnit)
	cogs, okCogs := resolve(RoleICCOGS, buyers, sellers, !sellerIsUnit)
	inv, okInv := resolve(RoleICInventory, buyers, sellers, !sellerIsUnit)
	f.salesUnit, f.salesGroup = sales.unit.Neg(), sales.group.Neg()
	f.costUnit, f.costGroup, f.costKnown = cost.unit, cost.group, okCost
	f.receivableUnit, f.receivableGroup = recv.unit, recv.group
	f.payableUnit, f.payableGroup = pay.unit.Neg(), pay.group.Neg()
	f.purchasesGroup, f.cogsGroup = purch.group, cogs.group
	f.inventoryGroup, f.hasInventory = inv.group, okInv
	if okSales && !okCost && !sellerIsUnit {
		// A holder's cost of sales that no line names: share the account total out by sales.
		totCost, totSales := sumRole(in.Lines, sellers, RoleICCost), sumRole(in.Lines, sellers, RoleICSales)
		if totCost.n > 0 && totSales.group.IsNegative() && f.salesGroup.IsPositive() {
			f.costGroup = totCost.group.Mul(f.salesGroup).Div(totSales.group.Neg())
			f.costKnown = true
			warn(CodeICUntagged, "the seller's cost of sales is not tagged with a partner; its ic_cost account total is shared out by sales to give this pair's margin")
		}
	}
	// The reverse direction, for E2 only: the buyer's receivable from the seller and the seller's payable to the buyer.
	revRecv := sumPartner(in.PartnerLines, buyers, sellers, RoleICReceivable)
	revPay := sumPartner(in.PartnerLines, sellers, buyers, RoleICPayable)
	forward := okSales || okCost || okRecv || okPay || okPurch || okCogs || okInv
	reverse := revRecv.n > 0 || revPay.n > 0
	if !forward && !reverse {
		return res
	}
	res.Applied = true
	// A balance on the wrong side (a payment that was not tagged took more than the tagged invoice) is no elimination amount: leave it out.
	for name, v := range map[string]*decimal.Decimal{"ic_sales": &f.salesGroup, "ic_cost": &f.costGroup, "ic_receivable": &f.receivableGroup, "ic_payable": &f.payableGroup} {
		if v.IsNegative() {
			warn(CodeICUntagged, "the "+name+" balance of the pair is on the wrong side after the account totals were applied and is left out")
			*v = decimal.Zero
		}
	}
	for name, v := range map[string]*decimal.Decimal{"ic_sales": &f.salesUnit, "ic_cost": &f.costUnit, "ic_receivable": &f.receivableUnit, "ic_payable": &f.payableUnit} {
		if v.IsNegative() {
			_ = name
			*v = decimal.Zero
		}
	}
	if reverse {
		rr, rp := revRecv.group, revPay.group.Neg()
		if rr.IsNegative() || rp.IsNegative() {
			warn(CodeICUntagged, "a balance in the reverse direction of the pair (the buyer's receivable from the seller, the seller's payable to the buyer) is on the wrong side and is left out")
		} else {
			pair.ReverseReceivableGroup, pair.ReversePayableGroup = decPtr(rr), decPtr(rp)
		}
	}
	if sellerIsUnit {
		// The subsidiary sells: its own currency is the seller's, the parent's payable is in the group currency as collected.
		if okSales {
			pair.ICSales = f.salesUnit
		}
		if okCost {
			pair.ICCost = f.costUnit
		}
		if okRecv {
			pair.ICReceivable = f.receivableUnit
		}
		if okPay {
			pair.PayableGroup = f.payableGroup
		}
	} else {
		// The parent sells: its sales, cost and receivable are in the group currency, the subsidiary's payable is not. The subsidiary's own
		// ic_receivable and ic_payable that the unit's role totals gave are the other direction of the pair (the reverse amounts below), so
		// the unit-currency fields start from zero.
		pair.ICSales, pair.ICCost, pair.ICReceivable, pair.PayableGroup = decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
		if okSales {
			pair.ICSalesGroup = decPtr(f.salesGroup)
		}
		if okCost || f.costKnown {
			pair.ICCostGroup = decPtr(f.costGroup)
		}
		if okRecv {
			pair.ReceivableGroup = decPtr(f.receivableGroup)
		}
		if okPay {
			pair.PayableUnit = decPtr(f.payableUnit)
		}
	}
	res.CostKnown = f.costKnown
	res.Flows = f
	profit, inventory, margin, ok := UnrealizedFromPartners(f, in.GroupCurrency)
	res.Estimate, res.EstimateOK = profitEstimate{Profit: profit, Inventory: inventory, MarginPct: margin}, ok
	if !ok && okSales {
		warn(CodeProfitEstimated, "the seller's cost of sales is unknown (no ic_cost line with a partner and no ic_cost account total), so no margin and no unrealised profit can be worked out; give receipt layers or an unsold share with the cost")
	}
	return res
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
