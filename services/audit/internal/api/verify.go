package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

// dutyVerify verifies the change chain (AUD-API-4).
const dutyVerify = "audit.verify"

// verifyChanges is GET /v1/audit/records:verify?fromSeq=&toSeq=. Without a range it walks the tenant's whole change
// chain from the genesis hash; fromSeq and toSeq (inclusive, positive) bound it to a seq range whose first row's
// prevHash is trusted. The verdict names the first break by seq and id, counts the known legacy breaks apart and
// checks the daily anchors in the range. It walks every legal entity's rows of the tenant (the chain is one per
// tenant) and returns no row content.
func (a *API) verifyChanges(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var bounds [2]int64
	for i, name := range []string{"fromSeq", "toSeq"} {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			continue
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 1 {
			badRange(w, r, name, "must be a positive integer")
			return
		}
		bounds[i] = v
	}
	if bounds[0] > 0 && bounds[1] > 0 && bounds[1] < bounds[0] {
		badRange(w, r, "toSeq", "must not be before fromSeq")
		return
	}
	chain, err := a.store.VerifyChanges(r.Context(), bounds[0], bounds[1])
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !chain.Verified {
		p, _ := tenant.FromContext(r.Context())
		slog.Warn("change chain verification failed", "tenantId", p.TenantID, "breaks", chain.Breaks, "firstBreak", chain.FirstBreak,
			"correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.JSON(w, http.StatusOK, chain)
}

func badRange(w http.ResponseWriter, r *http.Request, name, msg string) {
	pr := httpx.NewProblem(http.StatusBadRequest, domain.CodeVerifyRange, "the verification range is not valid")
	pr.Errors = map[string]string{name: msg}
	httpx.Fail(w, r, pr)
}
