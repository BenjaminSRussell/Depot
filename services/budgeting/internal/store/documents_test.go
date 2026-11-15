package store

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestDocumentRegister_ChecksNumberDocumentsAndKeepLineIDs(t *testing.T) {
	ctx, st, _, rule := setup(t)
	req := checkReq(rule.ID, "500")
	req.Lines = append(req.Lines, CheckLine{LineID: ids.New(), DimensionKey: "dept-10", SpanKey: "FY2026", Amount: d("200")})
	if _, _, err := st.CheckBudget(ctx, "po-1", "h", req, false); err != nil {
		t.Fatal(err)
	}
	second := checkReq(rule.ID, "50")
	second.DocumentType = "Requisition"
	if _, _, err := st.CheckBudget(ctx, "req-1", "h", second, false); err != nil {
		t.Fatal(err)
	}
	docs, err := st.ListDocuments(ctx, DocumentFilter{})
	if err != nil || len(docs) != 2 || docs[0].ID != second.DocumentID || !strings.HasPrefix(docs[0].Number, "BD-") || docs[1].LineCount != 2 {
		t.Fatalf("documents = %+v, %v", docs, err)
	}
	po := docs[1]
	if found, err := st.ListDocuments(ctx, DocumentFilter{Type: "PurchaseOrder", Q: strings.ToLower(po.Number[3:])}); err != nil || len(found) != 1 || found[0].ID != req.DocumentID {
		t.Fatalf("by type and number part = %+v, %v", found, err)
	}
	if found, err := st.ListDocuments(ctx, DocumentFilter{Number: po.Number}); err != nil || len(found) != 1 || found[0].ID != po.ID {
		t.Fatalf("by exact number = %+v, %v", found, err)
	}
	// A re-check of a revised document keeps its number and line ids; the line shows the new amount.
	req.Lines[0].Amount = d("900")
	if _, _, err := st.CheckBudget(ctx, "po-2", "h", req, false); err != nil {
		t.Fatal(err)
	}
	if again, err := st.GetDocument(ctx, req.DocumentID); err != nil || again.Number != po.Number || again.LineCount != 2 {
		t.Fatalf("after re-check = %+v, %v", again, err)
	}
	lines, err := st.ListDocumentLines(ctx, DocumentLineFilter{DocumentID: req.DocumentID})
	if err != nil || len(lines) != 2 || lines[0].LineNo != 1 || lines[0].ID != req.Lines[0].LineID || !lines[0].Amount.Equal(d("900")) || lines[0].DocumentNumber != po.Number {
		t.Fatalf("lines = %+v, %v", lines, err)
	}
	if one, err := st.GetDocumentLine(ctx, req.Lines[1].LineID); err != nil || one.LineNo != 2 || one.DimensionKey != "dept-10" {
		t.Fatalf("line = %+v, %v", one, err)
	}
	if found, err := st.ListDocumentLines(ctx, DocumentLineFilter{DocumentID: req.DocumentID, Q: "fy2026"}); err != nil || len(found) != 2 {
		t.Fatalf("line search = %+v, %v", found, err)
	}
	// Another tenant sees none of it.
	other := testCtx(testkit.TenantID(2), "budget.view")
	if list, err := st.ListDocuments(other, DocumentFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("another tenant sees %+v, %v", list, err)
	}
	if _, err := st.GetDocument(other, req.DocumentID); !hasCode(err, CodeDocumentNotFound) {
		t.Fatalf("cross-tenant get: %v", err)
	}
}
