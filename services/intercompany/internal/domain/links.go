package domain

import (
	"strings"
	"time"
)

const (
	DocPackingSlip   = "packing_slip"
	DocReceipt       = "receipt"
	DocInvoice       = "invoice"
	DocVendorInvoice = "vendor_invoice"
)

const (
	LinkLinked      = "linked"
	LinkCompensated = "compensated"
)

const (
	CodeLinkNotFound = "IC-0865"
	CodeLinkConflict = "IC-0866"
)

const maxLinkField = 64

var mirrorMatrix = map[string]string{
	DocSalesOrder:  DocPurchaseOrder,
	DocPackingSlip: DocReceipt,
	DocInvoice:     DocVendorInvoice,
}

type DocumentLink struct {
	ID           string
	PairID       string
	SourceEntity string
	SourceType   string
	SourceID     string
	MirrorEntity string
	MirrorType   string
	MirrorID     string
	PricingBasis string
	Status       string
	CreatedBy    string
	CreatedAt    time.Time
}

func (l DocumentLink) Validate() error {
	want, ok := mirrorMatrix[l.SourceType]
	if !ok {
		return invalid("sourceType " + l.SourceType + " has no mirror document")
	}
	if l.MirrorType != want {
		return invalid("a " + l.SourceType + " mirrors to a " + want + ", not a " + l.MirrorType)
	}
	for name, v := range map[string]string{"pairId": l.PairID, "sourceEntity": l.SourceEntity, "sourceId": l.SourceID, "mirrorEntity": l.MirrorEntity, "mirrorId": l.MirrorID} {
		if strings.TrimSpace(v) == "" || len(v) > maxLinkField {
			return invalid(name + " must be 1 to 64 characters")
		}
	}
	if l.SourceEntity == l.MirrorEntity {
		return invalid("a mirror document belongs to the other entity")
	}
	if len(l.PricingBasis) > maxLinkField {
		return invalid("pricingBasis must be at most 64 characters")
	}
	return nil
}

func (l DocumentLink) SameDocuments(o DocumentLink) bool {
	return l.SourceType == o.SourceType && l.SourceID == o.SourceID && l.MirrorType == o.MirrorType && l.MirrorID == o.MirrorID &&
		l.SourceEntity == o.SourceEntity && l.MirrorEntity == o.MirrorEntity
}
