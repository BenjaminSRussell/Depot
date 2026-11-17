package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/BenjaminSRussell/depot/platform/money"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Target load of the onhand and bank_balances packs (#49, H-S9-1, H-S10-1). The staged opening stock goes to the
// inventory ledger as OPENING_BALANCE journals (one batch of at most 1000 lines per currency), the staged opening bank
// balances to treasury (one batch of at most 1000 accounts). Each target stores a batch under a key and reports its
// batches back, which is what the factory reconciles against.

// Targets of the opening load.
const (
	TargetInventory = "inventory"
	TargetTreasury  = "treasury"
)

// MaxOpeningBatchItems is the most items one opening batch takes, in both targets.
const MaxOpeningBatchItems = 1000

// OpeningKeyHash is the length of the content hash at the end of a batch key.
const OpeningKeyHash = 24

// TargetOfPack returns the target an opening pack loads into, or "" for a pack with none.
func TargetOfPack(p Pack) string {
	switch p {
	case PackOnhand:
		return TargetInventory
	case PackBankBal:
		return TargetTreasury
	}
	return ""
}

// StockItem is one line of an opening stock batch: the quantity at the line's dimensions and its unit cost. The
// extract carries the total value of the quantity, so the unit cost is that value over the quantity at six decimals;
// the target values the line at quantity times that cost (the difference to the legacy value is under half a millionth
// of a unit of currency per unit of quantity and is reported by StockRounding).
type StockItem struct {
	Reference   string            `json:"reference"`
	ProductID   string            `json:"productId"`
	Dimensions  map[string]string `json:"dimensions"`
	EnteredUnit string            `json:"enteredUnit"`
	Qty         string            `json:"qty"`
	CostPrice   string            `json:"costPrice"`
	Currency    string            `json:"-"`
}

// Hash identifies the item as the target would store it.
func (it StockItem) Hash() string {
	dims := make([]string, 0, len(it.Dimensions))
	for k, v := range it.Dimensions {
		dims = append(dims, k+"="+v)
	}
	sort.Strings(dims)
	return hashParts(it.Reference, strings.ToLower(it.ProductID), strings.Join(dims, ","), it.EnteredUnit, it.Qty, it.CostPrice, it.Currency)
}

// UnitCost is the unit cost of an on-hand line: its value over its quantity, at six decimals.
func UnitCost(o OnhandLine) decimal.Decimal {
	return o.Value.DivRound(o.Quantity, 6)
}

// StockValue is the value the target books for a line: quantity times the rounded unit cost.
func StockValue(o OnhandLine) decimal.Decimal { return o.Quantity.Mul(UnitCost(o)) }

// BuildStockItem maps a staged on-hand line onto a batch item. products maps a legacy item to the Depot product id
// (an item that is itself a UUID needs no entry).
func BuildStockItem(o OnhandLine, products map[string]string) (StockItem, string, string) {
	product := strings.TrimSpace(products[o.Item])
	if product == "" && uuidPattern.MatchString(o.Item) {
		product = o.Item
	}
	if !uuidPattern.MatchString(product) {
		return StockItem{}, CodeRefUnresolved, fmt.Sprintf("item %q has no Depot product id: name it in products", o.Item)
	}
	dims := map[string]string{"site": o.Site, "warehouse": o.Warehouse}
	for k, v := range map[string]string{"location": o.Location, "batch": o.Batch, "serial": o.Serial} {
		if v != "" {
			dims[k] = v
		}
	}
	return StockItem{Reference: o.LegacyID, ProductID: strings.ToLower(product), Dimensions: dims, EnteredUnit: o.Unit,
		Qty: o.Quantity.String(), CostPrice: UnitCost(o).String(), Currency: o.Currency}, "", ""
}

// BankItem is one account of an opening balance batch: the book balance in minor units of the account's currency.
type BankItem struct {
	Reference     string `json:"reference"`
	BankAccountID string `json:"bankAccountId"`
	Currency      string `json:"currency"`
	BalanceMinor  int64  `json:"balanceMinor"`
}

// Hash identifies the item as treasury would store it.
func (it BankItem) Hash() string {
	return hashParts(it.Reference, strings.ToLower(it.BankAccountID), it.Currency, fmt.Sprint(it.BalanceMinor))
}

// Reject codes of the bank balance load.
const CodeBankPrecision = "MIG-LOAD-BANK-PRECISION"

// BuildBankItem maps a staged bank balance onto a batch item. accounts maps a legacy bank account to the treasury
// bank account id (an account that is itself a UUID needs no entry).
func BuildBankItem(b BankBalanceLine, accounts map[string]string) (BankItem, string, string) {
	account := strings.TrimSpace(accounts[b.BankAccount])
	if account == "" && uuidPattern.MatchString(b.BankAccount) {
		account = b.BankAccount
	}
	if !uuidPattern.MatchString(account) {
		return BankItem{}, CodeRefUnresolved, fmt.Sprintf("bank account %q has no treasury bank account id: name it in bankAccounts", b.BankAccount)
	}
	minor, ok := toMinor(b.Balance, b.Currency)
	if !ok {
		return BankItem{}, CodeBankPrecision, fmt.Sprintf("balance %s is not a whole number of minor units of %s or does not fit treasury's column", b.Balance, b.Currency)
	}
	return BankItem{Reference: b.LegacyID, BankAccountID: strings.ToLower(account), Currency: b.Currency, BalanceMinor: minor}, "", ""
}

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// OpeningKeyPrefix starts every batch key the factory writes for a pack of a legacy company. The company is hashed
// so that any legacy code makes a valid key.
func OpeningKeyPrefix(p Pack, system, company string) string {
	sum := sha256.Sum256([]byte(company))
	return "mf-" + string(p) + "-" + system + "-" + hex.EncodeToString(sum[:])[:12] + "-"
}

// OpeningBatchKey is the idempotency key of one batch: stable for the same items, so a retry, or a later load of an
// unchanged snapshot, is the same request to the target. currency is part of an inventory key (a batch holds one) and
// empty in a treasury key.
func OpeningBatchKey(p Pack, system, company, currency string, hashes []string) string {
	sum := sha256.Sum256([]byte(strings.Join(hashes, ",")))
	return OpeningKeyPrefix(p, system, company) + currency + "-" + hex.EncodeToString(sum[:])[:OpeningKeyHash]
}

// IsOpeningBatchKey reports whether a batch key was written by the factory for the pack of the company.
func IsOpeningBatchKey(p Pack, system, company, key string) bool {
	prefix := OpeningKeyPrefix(p, system, company)
	if !strings.HasPrefix(key, prefix) || len(key) < len(prefix)+1+OpeningKeyHash {
		return false
	}
	rest := key[len(prefix):]
	return rest[len(rest)-OpeningKeyHash-1] == '-' && len(rest)-OpeningKeyHash-1 <= 3
}

// OpeningLoad is the load of one onhand or bank_balances run.
type OpeningLoad struct {
	RunID         string
	Target        string
	LegalEntityID string
	Status        string
	Staged        int
	Loaded        int
	Rejected      int
	Pending       int
	Attempts      int
	LastError     string
	StartedBy     string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	Version       int
	CreatedAt     time.Time
}

// Clean reports that every staged balance reached the target and its totals match.
func (l OpeningLoad) Clean() bool {
	return l.Status == LoadReconciled && l.Pending == 0 && l.Rejected == 0
}

// OpeningTotals is what a target holds for one pack of a company: the item count, and per currency the quantity and
// value (stock) or the balance in minor units (bank).
type OpeningTotals struct {
	Items int
	Qty   map[string]decimal.Decimal
	Value map[string]decimal.Decimal
	Minor map[string]int64
}

// NewOpeningTotals returns empty totals.
func NewOpeningTotals() OpeningTotals {
	return OpeningTotals{Qty: map[string]decimal.Decimal{}, Value: map[string]decimal.Decimal{}, Minor: map[string]int64{}}
}

// ReconcileStock compares what the factory loaded (expected) with what the inventory ledger reports for the
// company's batches (actual): the item count, and per currency the quantity and the value, exactly.
func ReconcileStock(legalEntityID string, expected, actual OpeningTotals) ([]ControlTotal, error) {
	key := map[string]string{"legalEntityId": legalEntityID}
	count, err := NewControlTotal("target.onhand.item_count", key, decimal.NewFromInt(int64(expected.Items)), decimal.NewFromInt(int64(actual.Items)), Tolerance{})
	if err != nil {
		return nil, err
	}
	out := []ControlTotal{count}
	for _, c := range unionKeys(expected.Qty, actual.Qty, expected.Value, actual.Value) {
		ck := map[string]string{"legalEntityId": legalEntityID, "currency": c}
		q, err := NewControlTotal("target.onhand.quantity", ck, expected.Qty[c], actual.Qty[c], Tolerance{})
		if err != nil {
			return nil, err
		}
		v, err := NewControlTotal("target.onhand.value", ck, expected.Value[c], actual.Value[c], Tolerance{})
		if err != nil {
			return nil, err
		}
		out = append(out, q, v)
	}
	return out, nil
}

// OnHandStock is what the inventory ledger's on-hand inquiry reports for one product in the legal entity: the physical
// quantity and the posted value per currency.
type OnHandStock struct {
	Qty   decimal.Decimal
	Value map[string]decimal.Decimal
}

// ReconcileOnHand is the on-hand read-back of BR-49.3 (H-S9-2): per product, the quantity the factory's accepted opening batches put
// in the legal entity against what the inventory ledger's on-hand inquiry reports after the import. Products are Depot ids. Stock
// moved after the load, or loaded into the entity from outside the factory, shows as a difference.
func ReconcileOnHand(legalEntityID string, loaded map[string]decimal.Decimal, onHand map[string]OnHandStock) ([]ControlTotal, error) {
	products := make([]string, 0, len(loaded))
	for p := range loaded {
		products = append(products, p)
	}
	for p := range onHand {
		if _, ok := loaded[p]; !ok {
			products = append(products, p)
		}
	}
	sort.Strings(products)
	out := make([]ControlTotal, 0, len(products))
	for _, p := range products {
		t, err := NewControlTotal("target.onhand.onhand_quantity", map[string]string{"legalEntityId": legalEntityID, "product": p}, loaded[p], onHand[p].Qty, Tolerance{})
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// NoInventoryAccount is the account label of on-hand value loaded by an onhand run that named no inventory account.
const NoInventoryAccount = "(none named)"

// AccountValue is the value the factory's accepted onhand batches put in the legal entity for one inventory account and
// currency: the value of the stock lines of the runs that named the account (quantity times unit cost), the number of
// lines and the products they hold.
type AccountValue struct {
	Account  string
	Currency string
	Value    decimal.Decimal
	Lines    int
	Products map[string]struct{}
}

// OnHandValueRounding is the rounding the on-hand value read-back allows for a currency: half a minor unit for each line the ledger
// valued, and at least one minor unit. The ledger posts each line's value at the currency's own precision (two decimals for USD, none
// for JPY, three for KWD) while the factory sums quantity times a unit cost of six decimals, so a larger difference is a real one.
func OnHandValueRounding(currency string, lines int) decimal.Decimal {
	unit := decimal.New(1, -int32(money.MinorUnits(currency)))
	half := unit.Mul(decimal.RequireFromString("0.5")).Mul(decimal.NewFromInt(int64(lines))).RoundCeil(int32(money.MinorUnits(currency)))
	return decimal.Max(half, unit)
}

// ReconcileOnHandValue is the value half of the on-hand read-back (#49, BR-49.3): per inventory account and currency, the
// value the factory loaded against the posted value the inventory ledger reports for that account's products after the import.
// The ledger reports value per product, not per account, so accounts that share a product are compared together (labelled
// "1100+1200") instead of each being charged the whole product. It is MISMATCH when the two differ by more than OnHandValueRounding.
// Stock revalued, issued or received after the load shows as a difference, like a quantity difference does.
func ReconcileOnHandValue(legalEntityID string, expected []AccountValue, onHand map[string]OnHandStock) ([]ControlTotal, error) {
	byCurrency := map[string][]AccountValue{}
	var currencies []string
	for _, a := range expected {
		if _, ok := byCurrency[a.Currency]; !ok {
			currencies = append(currencies, a.Currency)
		}
		byCurrency[a.Currency] = append(byCurrency[a.Currency], a)
	}
	sort.Strings(currencies)
	var out []ControlTotal
	for _, cur := range currencies {
		accounts := byCurrency[cur]
		// Connected groups of accounts that share a product.
		parent := make([]int, len(accounts))
		for i := range parent {
			parent[i] = i
		}
		var find func(int) int
		find = func(i int) int {
			for parent[i] != i {
				parent[i] = parent[parent[i]]
				i = parent[i]
			}
			return i
		}
		owner := map[string]int{}
		for i, a := range accounts {
			for p := range a.Products {
				if j, ok := owner[p]; ok {
					parent[find(i)] = find(j)
				} else {
					owner[p] = i
				}
			}
		}
		groups := map[int]*AccountValue{}
		labels := map[int][]string{}
		for i, a := range accounts {
			r := find(i)
			g := groups[r]
			if g == nil {
				g = &AccountValue{Currency: cur, Products: map[string]struct{}{}}
				groups[r] = g
			}
			g.Value, g.Lines = g.Value.Add(a.Value), g.Lines+a.Lines
			for p := range a.Products {
				g.Products[p] = struct{}{}
			}
			labels[r] = append(labels[r], a.Account)
		}
		roots := make([]int, 0, len(groups))
		for r := range groups {
			sort.Strings(labels[r])
			roots = append(roots, r)
		}
		sort.Slice(roots, func(i, j int) bool { return strings.Join(labels[roots[i]], "+") < strings.Join(labels[roots[j]], "+") })
		for _, r := range roots {
			g := groups[r]
			actual := decimal.Zero
			for p := range g.Products {
				actual = actual.Add(onHand[p].Value[cur])
			}
			t, err := NewControlTotal("target.onhand.onhand_value", map[string]string{"legalEntityId": legalEntityID, "account": strings.Join(labels[r], "+"), "currency": cur},
				g.Value, actual, Tolerance{Amount: OnHandValueRounding(cur, g.Lines), Reason: "the ledger posts each stock line's value at the currency's precision"})
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Key["account"] != out[j].Key["account"] {
			return out[i].Key["account"] < out[j].Key["account"]
		}
		return out[i].Key["currency"] < out[j].Key["currency"]
	})
	return out, nil
}

// ReconcileBank compares the accounts and the balance per currency in minor units that treasury holds with what was loaded.
func ReconcileBank(legalEntityID string, expected, actual OpeningTotals) ([]ControlTotal, error) {
	key := map[string]string{"legalEntityId": legalEntityID}
	count, err := NewControlTotal("target.bank_balances.item_count", key, decimal.NewFromInt(int64(expected.Items)), decimal.NewFromInt(int64(actual.Items)), Tolerance{})
	if err != nil {
		return nil, err
	}
	out := []ControlTotal{count}
	curs := map[string]bool{}
	for c := range expected.Minor {
		curs[c] = true
	}
	for c := range actual.Minor {
		curs[c] = true
	}
	names := make([]string, 0, len(curs))
	for c := range curs {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		t, err := NewControlTotal("target.bank_balances.total_minor", map[string]string{"legalEntityId": legalEntityID, "currency": c},
			decimal.NewFromInt(expected.Minor[c]), decimal.NewFromInt(actual.Minor[c]), Tolerance{})
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func unionKeys(maps ...map[string]decimal.Decimal) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ToMinor converts an amount to whole minor units of the currency; ok is false for a fraction of a minor unit or a value
// above what a JSON integer carries.
func ToMinor(amount decimal.Decimal, currency string) (int64, bool) { return toMinor(amount, currency) }
