package domain

import "sort"

var readDuties = map[string]string{
	"party.party":         "party.read",
	"party.vendor":        "party.read",
	"party.customer":      "party.read",
	"vendor_bank_account": "party.read",
	"product.product":     "product.read",
	"purchase_order":      "procurement.purchase-order.inquire",
	"receipt":             "receiving.receipt.maintain",
	"ap_invoice":          "receiving.ap.inquire",
	"sales_order":         "sales.order.read",
	"customer_invoice":    "billing.invoice.view",
	"journal_entry":       "ledger.journal.read",
	"production_order":    "production.order.read",
	"worker":              "hr.worker.inquire",
}

var sensitiveFields = map[string]bool{
	"vendor_bank_account/account_number": true,
	"vendor_bank_account/iban":           true,
	"party.vendor/bank_account_number":   true,
	"party.customer/credit_limit":        true,
	"party.party/tax_registration":       true,
	"product.product/cost":               true,
}

func ReadDuty(entity string) (string, bool) {
	d, ok := readDuties[entity]
	return d, ok
}

func KnownEntities() []string {
	out := make([]string, 0, len(readDuties))
	for e := range readDuties {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

func IsSensitive(entity, field string) bool { return sensitiveFields[entity+"/"+field] }

func Mask(value string) string {
	if value == "" {
		return ""
	}
	if r := []rune(value); len(r) >= 8 {
		return "****" + string(r[len(r)-4:])
	}
	return "****"
}
