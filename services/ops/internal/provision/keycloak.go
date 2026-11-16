package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Keycloak is the RealmAdmin over the Keycloak Admin REST API. It takes a token with the client-credentials grant of a configured
// admin client (in AdminRealm, normally "master"), then creates the realm "<RealmPrefix><tenant id>" from TemplatePath (the realm
// export deploy/keycloak/realm-passage.json, with the tenant substituted) or, without a template, from a minimal realm that holds
// the settings and the passage-services client identity and the services need. A realm that already exists is a success.
type Keycloak struct {
	BaseURL      string // https://keycloak.example/ (no /realms or /admin suffix)
	AdminRealm   string // default master
	ClientID     string
	ClientSecret string
	RealmPrefix  string // default "passage-"
	TemplatePath string // optional realm JSON; REPLACE_WITH_TENANT_ID is replaced by the tenant id
	HTTP         *http.Client
}

// ErrRealmTemplate is returned when the realm template cannot be used.
var ErrRealmTemplate = fmt.Errorf("realm template")

func (k Keycloak) prefix() string {
	if k.RealmPrefix == "" {
		return "passage-"
	}
	return k.RealmPrefix
}

// RealmName is the realm of a tenant.
func (k Keycloak) RealmName(t Tenant) (string, error) {
	if err := checkTenantID(t.ID); err != nil {
		return "", err
	}
	name := k.prefix() + t.ID
	if t.Realm != "" {
		name = t.Realm
	}
	if name == "master" || strings.EqualFold(name, k.AdminRealm) {
		return "", fmt.Errorf("realm name %q would be the admin realm", name)
	}
	return name, nil
}

func (k Keycloak) base() string { return strings.TrimSuffix(k.BaseURL, "/") }

func (k Keycloak) token(ctx context.Context) (string, error) {
	realm := k.AdminRealm
	if realm == "" {
		realm = "master"
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {k.ClientID}, "client_secret": {k.ClientSecret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.base()+"/realms/"+url.PathEscape(realm)+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClientOr(k.HTTP).Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak token: status %d (check the admin client and its secret)", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("keycloak token: no access_token in the response")
	}
	return out.AccessToken, nil
}

// realmBody builds the representation to import.
func (k Keycloak) realmBody(t Tenant, realm string) (map[string]any, error) {
	if k.TemplatePath == "" {
		return minimalRealm(t, realm), nil
	}
	raw, err := os.ReadFile(k.TemplatePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRealmTemplate, err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "REPLACE_WITH_TENANT_ID", t.ID)), &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRealmTemplate, err)
	}
	doc["realm"] = realm
	doc["id"] = realm
	if t.Name != "" {
		doc["displayName"] = t.Name
	}
	doc["enabled"] = true
	// Corporate identity providers in the template are placeholders for one real tenant; a new tenant's own IdP is added after
	// provisioning, so the template's are not imported (they would carry another tenant's example endpoints).
	delete(doc, "identityProviders")
	delete(doc, "identityProviderMappers")
	// The service account of passage-services has no tenant_id attribute, so its tid claim is fixed to the tenant, as in the minimal realm.
	if clients, ok := doc["clients"].([]any); ok {
		for _, c := range clients {
			cl, ok := c.(map[string]any)
			if !ok || cl["clientId"] != "passage-services" {
				continue
			}
			var keep []any
			for _, m := range asSlice(cl["protocolMappers"]) {
				if mm, ok := m.(map[string]any); ok && mm["name"] == "tid" {
					continue
				}
				keep = append(keep, m)
			}
			cl["protocolMappers"] = append(keep, tidMapper(t.ID))
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRealmTemplate, err)
	}
	if strings.Contains(string(out), "REPLACE_WITH_") {
		return nil, fmt.Errorf("%w: it still has a REPLACE_WITH_ placeholder (other than REPLACE_WITH_TENANT_ID) outside identityProviders and identityProviderMappers", ErrRealmTemplate)
	}
	return doc, nil
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func tidMapper(tenantID string) map[string]any {
	return map[string]any{"name": "tid-hardcoded", "protocol": "openid-connect", "protocolMapper": "oidc-hardcoded-claim-mapper",
		"config": map[string]string{"claim.name": "tid", "claim.value": tenantID, "jsonType.label": "String", "id.token.claim": "true", "access.token.claim": "true"}}
}

func minimalRealm(t Tenant, realm string) map[string]any {
	name := t.Name
	if name == "" {
		name = realm
	}
	mapper := func(n, attr string) map[string]any {
		return map[string]any{"name": n, "protocol": "openid-connect", "protocolMapper": "oidc-usermodel-attribute-mapper",
			"config": map[string]string{"user.attribute": attr, "claim.name": n, "jsonType.label": "String", "id.token.claim": "true", "access.token.claim": "true", "userinfo.token.claim": "true"}}
	}
	return map[string]any{
		"realm": realm, "id": realm, "displayName": name, "enabled": true,
		"sslRequired": "external", "registrationAllowed": false, "loginWithEmailAllowed": true, "duplicateEmailsAllowed": false,
		"bruteForceProtected": true, "accessTokenLifespan": 300, "revokeRefreshToken": true, "ssoSessionIdleTimeout": 1800, "ssoSessionMaxLifespan": 36000,
		"defaultSignatureAlgorithm": "RS256", "passwordPolicy": "hashAlgorithm(pbkdf2-sha512) and length(14) and notUsername",
		"eventsEnabled": true, "adminEventsEnabled": true,
		"roles": map[string]any{"realm": []map[string]string{{"name": "supplier-user"}, {"name": "supplier-admin"}, {"name": "customer-user"}}},
		"clients": []map[string]any{{
			"clientId": "passage-services", "name": "Passage services and SCIM", "enabled": true, "protocol": "openid-connect",
			"publicClient": false, "standardFlowEnabled": false, "implicitFlowEnabled": false, "directAccessGrantsEnabled": false, "serviceAccountsEnabled": true,
			"protocolMappers": []map[string]any{
				{"name": "audience-passage-api", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
					"config": map[string]string{"included.custom.audience": "passage-api", "id.token.claim": "false", "access.token.claim": "true"}},
				tidMapper(t.ID),
				mapper("le", "legal_entity_id"), mapper("duties", "duties"), mapper("tier", "tier"),
			},
		}},
	}
}

func (k Keycloak) EnsureRealm(ctx context.Context, t Tenant, dry bool) (string, error) {
	realm, err := k.RealmName(t)
	if err != nil {
		return "", err
	}
	src := "a minimal realm with the passage-services client"
	if k.TemplatePath != "" {
		src = "the realm template " + k.TemplatePath
	}
	body, err := k.realmBody(t, realm)
	if err != nil {
		return "", err
	}
	if dry {
		return fmt.Sprintf("would create Keycloak realm %s from %s unless it exists (admin client %s in realm %s)", realm, src, k.ClientID, orDefault(k.AdminRealm, "master")), nil
	}
	tok, err := k.token(ctx)
	if err != nil {
		return "", err
	}
	st, b, err := doJSON(ctx, k.HTTP, http.MethodGet, k.base()+"/admin/realms/"+url.PathEscape(realm), tok, nil)
	if err != nil {
		return "", fmt.Errorf("keycloak: %w", err)
	}
	switch {
	case st == http.StatusOK:
		return "realm " + realm + " already exists", nil
	case st != http.StatusNotFound:
		return "", fmt.Errorf("keycloak: GET realm %s: status %d %s", realm, st, snippet(b))
	}
	st, b, err = doJSON(ctx, k.HTTP, http.MethodPost, k.base()+"/admin/realms", tok, body)
	if err != nil {
		return "", fmt.Errorf("keycloak: %w", err)
	}
	switch st {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return "created realm " + realm + " from " + src, nil
	case http.StatusConflict:
		return "realm " + realm + " already exists", nil
	}
	return "", fmt.Errorf("keycloak: create realm %s: status %d %s", realm, st, snippet(b))
}

func (k Keycloak) DeleteRealm(ctx context.Context, t Tenant, dry bool) (string, error) {
	realm, err := k.RealmName(t)
	if err != nil {
		return "", err
	}
	if dry {
		return "would delete Keycloak realm " + realm + " (absent is a success)", nil
	}
	tok, err := k.token(ctx)
	if err != nil {
		return "", err
	}
	st, b, err := doJSON(ctx, k.HTTP, http.MethodDelete, k.base()+"/admin/realms/"+url.PathEscape(realm), tok, nil)
	if err != nil {
		return "", fmt.Errorf("keycloak: %w", err)
	}
	switch st {
	case http.StatusNoContent, http.StatusOK:
		return "deleted realm " + realm, nil
	case http.StatusNotFound:
		if t.Realm == "" {
			return "", fmt.Errorf("keycloak: realm %s does not exist, and the realm created for this tenant was never recorded, so the name is a guess: find the tenant's realm in Keycloak and record it before decommissioning", realm)
		}
		return "realm " + realm + " was already absent", nil
	}
	return "", fmt.Errorf("keycloak: delete realm %s: status %d %s", realm, st, snippet(b))
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
