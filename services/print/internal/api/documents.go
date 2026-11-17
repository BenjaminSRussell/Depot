package api

import (
	"net/http"
	"strconv"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/print/internal/domain"
)

const (
	dutyRenderDocument = "print.document.render"
	// dutyRenderPortal is the customer portal's own, service-only duty for the same renderer (#30): it renders the invoice or
	// statement the portal already read for its customer. Rendering reads nothing, so it reaches no other data.
	dutyRenderPortal = "print.customer-portal.render"
	// dutyRenderOps is the ops control plane's service-only duty for the same renderer (#50): the tenant cost statement PDF.
	dutyRenderOps = "print.ops.render"
)

// renderDocument serves POST /print/v1/documents:render (issue #530). The caller sends a
// purchase order, customer invoice, packing slip or remittance advice (#528) as data; the
// answer is the PDF. Nothing
// is stored and no printer is involved, so the call needs no idempotency key and repeating
// it returns the same bytes. X-Depot-Document-Pages carries the page count, and
// X-Depot-Unprintable-Characters, when present, the number of characters the document
// fonts lack (each printed as the replacement character), so the caller can tell the user.
func (a *API) renderDocument(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req renderRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	composed, err := a.compose(r, req)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	doc, err := domain.RenderDocumentReport(composed)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="document.pdf"`)
	w.Header().Set("X-Depot-Document-Pages", strconv.Itoa(doc.Pages))
	if doc.MissingGlyphs > 0 {
		w.Header().Set("X-Depot-Unprintable-Characters", strconv.Itoa(doc.MissingGlyphs))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc.PDF)
}
