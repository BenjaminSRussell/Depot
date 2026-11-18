package identity

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
)

const (
	codeSessionRevokeFailed  = "IDN-SESSION-REVOKE-FAILED"
	codeSessionRevokeRefused = "IDN-SESSION-REVOKE-REFUSED"
	codeSessionRevokeOff     = "IDN-SESSION-REVOKE-OFF"

	sessionRevocationReasonMax = 500
)

type SessionRevocationsAPI struct {
	pool     *pgxpool.Pool
	keycloak *KeycloakAdmin
}

func NewSessionRevocationsAPI(pool *pgxpool.Pool, keycloak *KeycloakAdmin) *SessionRevocationsAPI {
	return &SessionRevocationsAPI{pool: pool, keycloak: keycloak}
}

func (a *SessionRevocationsAPI) Register(r chi.Router) {
	r.With(requireAnyDuty(dutyMaintainUsers)).Post("/v1/identity/users/{id}/session-revocations", a.revoke)
}

type sessionRevocationRequest struct {
	Reason string `json:"reason"`
}

type sessionRevocation struct {
	UserID         string    `json:"userId"`
	Outcome        string    `json:"outcome"`
	KeycloakUserID string    `json:"keycloakUserId,omitempty"`
	RevokedBy      string    `json:"revokedBy"`
	RevokedAt      time.Time `json:"revokedAt"`
}

func (a *SessionRevocationsAPI) revoke(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	userID, ok := canonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		invalidFields(w, r, "user id must be a UUID", map[string]string{"id": "must be a UUID"})
		return
	}
	if !dbx.TenantWide(p, dutyMaintainUsers) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "ending a user's sessions needs a tenant administrator"))
		return
	}
	if a.keycloak == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeSessionRevokeOff, "Keycloak administration is not configured for identity"))
		return
	}
	var req sessionRevocationRequest
	if err := decodeOptional(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if utf8.RuneCountInString(req.Reason) > sessionRevocationReasonMax {
		invalidFields(w, r, "reason is too long", map[string]string{"reason": "must be at most 500 characters"})
		return
	}

	var target KeycloakTarget
	var realm string
	err := dbx.WithTenantWideAdminTx(r.Context(), a.pool, p, dutyMaintainUsers, func(tx pgx.Tx) error {
		target.TenantID = p.TenantID
		return tx.QueryRow(r.Context(), `select u.idp_subject, coalesce(u.user_name, ''), t.realm_name
from identity_users u join identity_tenants t on t.tenant_id = u.tenant_id
where u.tenant_id = $1 and u.user_id = $2`, p.TenantID, userID).Scan(&target.Subject, &target.UserName, &realm)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUserNotFound, "user not found"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	outcome, err := a.keycloak.RevokeSessions(r.Context(), realm, target)
	if err != nil {
		slog.Error("session revocation failed", "tenantId", p.TenantID, "userId", userID, "actor", p.UserID, "err", limitDetail(err.Error()))
		var kcErr *KeycloakError
		if errors.As(err, &kcErr) && kcErr.Permanent {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeSessionRevokeRefused, "Keycloak refused to end the sessions: "+kcErr.Detail))
			return
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, codeSessionRevokeFailed, "Keycloak could not end the sessions; nothing was recorded, retry"))
		return
	}

	result := sessionRevocation{UserID: userID, Outcome: outcome.Result, KeycloakUserID: outcome.UserID, RevokedBy: p.UserID, RevokedAt: time.Now().UTC()}
	err = dbx.WithTenantWideAdminTx(r.Context(), a.pool, p, dutyMaintainUsers, func(tx pgx.Tx) error {
		return record(r.Context(), tx, p.TenantID, p.UserID, "session", userID, "revoked", map[string]any{
			"userId": userID, "outcome": outcome.Result, "keycloakUserId": outcome.UserID, "reason": req.Reason,
		})
	})
	if err != nil {
		slog.Error("session revocation done in Keycloak but not recorded", "tenantId", p.TenantID, "userId", userID, "actor", p.UserID, "err", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}
