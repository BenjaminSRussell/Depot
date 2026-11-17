package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	dutyViewEffectivePermissions = "identity.effective-permission.view"
	dutyMaintainUsers            = "identity.user.maintain"
	codeInvalid                  = "COM-0001"
	codeUserNotFound             = "IDN-USER-NOT-FOUND"
)

// EffectivePermissionsAPI serves the read-only effective-permission report.
// A role assignment's role holds a duty name until the role catalogue exists
// (issue #83); roles that are not seeded duties are reported as ignored.
type EffectivePermissionsAPI struct {
	pool      *pgxpool.Pool
	catalogue *Catalogue
	// operatorTenantID is the tenant in which operator-only duties grant; empty means none.
	operatorTenantID string
}

func NewEffectivePermissionsAPI(pool *pgxpool.Pool, catalogue *Catalogue) *EffectivePermissionsAPI {
	return &EffectivePermissionsAPI{pool: pool, catalogue: catalogue}
}

// WithOperatorTenant names the operator tenant, the only tenant whose users can exercise operator-only duties; outside it
// such a duty is reported among the ignored roles, as authz.rego grants nothing for it there.
func (a *EffectivePermissionsAPI) WithOperatorTenant(operatorTenantID string) *EffectivePermissionsAPI {
	a.operatorTenantID = operatorTenantID
	return a
}

func requireAnyDuty(duties ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.FromContext(r.Context())
			if !ok {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
				return
			}
			for _, duty := range duties {
				if p.HasDuty(duty) {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+duties[0]))
		})
	}
}

func (a *EffectivePermissionsAPI) Register(r chi.Router) {
	r.With(requireAnyDuty(dutyViewEffectivePermissions, dutyMaintainUsers)).Get("/v1/identity/users/{id}/effective-permissions", a.get)
}

type effectivePermission struct {
	Resource    string `json:"resource"`
	Method      string `json:"method"`
	Allow       bool   `json:"allow"`
	Duty        string `json:"duty,omitempty"`
	DutyMissing string `json:"dutyMissing,omitempty"`
}

type effectivePermissionsResponse struct {
	UserID        string                `json:"userId"`
	LegalEntityID string                `json:"legalEntityId,omitempty"`
	Duties        []string              `json:"duties"`
	IgnoredRoles  []string              `json:"ignoredRoles"`
	Permissions   []effectivePermission `json:"permissions"`
}

// activeRolesQuery reads the user's active assignments that apply: tenant-wide ones, and ones scoped to the requested
// legal entity, with whether each is scoped and whether its legal entity is still active, so the report applies the
// bundle compiler's exclusions (B-03b).
const activeRolesQuery = `select a.role, a.legal_entity_id is not null, coalesce(le.status = 'active', a.legal_entity_id is null)
from identity_role_assignments a
left join identity_legal_entities le on le.tenant_id = a.tenant_id and le.legal_entity_id = a.legal_entity_id
where a.tenant_id = $1 and a.user_id = $2
  and a.valid_from <= now() and (a.valid_to is null or a.valid_to > now())
  and (a.legal_entity_id is null or a.legal_entity_id = $3::uuid)
order by a.role`

// assignedRole is one row of activeRolesQuery.
type assignedRole struct {
	role         string
	scoped       bool
	entityActive bool
}

func (a *EffectivePermissionsAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !validUUID(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	if dbx.LegalEntityScopes(p) != "*" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "effective permissions require access to all legal entities"))
		return
	}
	userID := chi.URLParam(r, "id")
	legalEntityID := r.URL.Query().Get("legalEntityId")
	fields := map[string]string{}
	if !validUUID(userID) {
		fields["id"] = "must be a UUID"
	}
	if legalEntityID != "" && !validUUID(legalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		problem := httpx.NewProblem(http.StatusBadRequest, codeInvalid, "user id and legalEntityId must be UUIDs")
		problem.Errors = fields
		httpx.Fail(w, r, problem)
		return
	}
	var legalEntity *string
	if legalEntityID != "" {
		legalEntity = &legalEntityID
	}

	var roles []assignedRole
	var exists bool
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `select exists(select 1 from identity_users where tenant_id = $1 and user_id = $2)`, p.TenantID, userID).Scan(&exists); err != nil || !exists {
			return err
		}
		rows, err := tx.Query(r.Context(), activeRolesQuery, p.TenantID, userID, legalEntity)
		if err != nil {
			return err
		}
		roles, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (assignedRole, error) {
			var a assignedRole
			err := row.Scan(&a.role, &a.scoped, &a.entityActive)
			return a, err
		})
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !exists {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUserNotFound, "user not found"))
		return
	}

	// The same exclusions as the bundle compiler and authz.rego (B-03b): a scoped assignment of a tenant-level duty, or
	// one whose legal entity is no longer active, grants nothing; a role counts when any of its rows grants.
	duties, ignored := []string{}, []string{}
	operator := a.operatorTenantID != "" && p.TenantID == a.operatorTenantID
	granting, seen := map[string]bool{}, map[string]bool{}
	var order []string
	for _, ar := range roles {
		role := ar.role
		if !seen[role] {
			seen[role] = true
			order = append(order, role)
		}
		usable := a.catalogue.HasDuty(role) && (operator || !a.catalogue.OperatorOnly(role))
		if ar.scoped && (!ar.entityActive || a.catalogue.TenantLevel(role)) {
			usable = false
		}
		if usable {
			granting[role] = true
		}
	}
	for _, role := range order {
		if granting[role] {
			duties = append(duties, role)
		} else {
			ignored = append(ignored, role)
		}
	}
	grants := a.catalogue.Effective(duties)
	permissions := make([]effectivePermission, len(grants))
	for i, g := range grants {
		permissions[i] = effectivePermission(g)
	}
	httpx.JSON(w, http.StatusOK, effectivePermissionsResponse{
		UserID: userID, LegalEntityID: legalEntityID,
		Duties: duties, IgnoredRoles: ignored, Permissions: permissions,
	})
}
