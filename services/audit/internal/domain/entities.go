package domain

import "sort"

// readDuties maps an audit entity to the duty that lets a person read that record (AUD-API-6). An entity that is
// not here is readable only with audit.read.
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

// ReadDuty is the duty that reads entity's records.
func ReadDuty(entity string) (string, bool) {
	d, ok := readDuties[entity]
	return d, ok
}

// KnownEntities lists the entities this service maps to a read duty, sorted.
func KnownEntities() []string {
	out := make([]string, 0, len(readDuties))
	for e := range readDuties {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// IsSensitive reports whether values of entity's field are masked for a caller without audit.export.
func IsSensitive(entity, field string) bool { return sensitiveFields[entity+"/"+field] }

// Mask hides all but the last four characters of a value of 8 or more characters, and all of a shorter one.
func Mask(value string) string {
	if value == "" {
		return ""
	}
	if r := []rune(value); len(r) >= 8 {
		return "****" + string(r[len(r)-4:])
	}
	return "****"
}
