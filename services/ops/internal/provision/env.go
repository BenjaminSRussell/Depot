package provision

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// FromEnv builds the executors a real run uses. Each system with its configuration present gets its real adapter; the others stay
// on the LogAdapter (which refuses a real run) and are named in Unconfigured, so the command can refuse before it touches the API.
// Kafka needs no configuration: the shared-topic model has nothing to provision per tenant.
//
//	Keycloak:  OPS_KEYCLOAK_URL, OPS_KEYCLOAK_CLIENT_ID, OPS_KEYCLOAK_CLIENT_SECRET; optional OPS_KEYCLOAK_ADMIN_REALM (master),
//	           OPS_KEYCLOAK_REALM_PREFIX (passage-), OPS_KEYCLOAK_REALM_TEMPLATE (path to a realm JSON)
//	Databases: OPS_PROVISION_DATABASES (JSON {"ledger": "postgres://..."} or @file), as passage_app. The purge has its own
//	           credentials: see PurgerFromEnv
//	Lakehouse: OPS_LAKEHOUSE_CATALOG_URL; optional OPS_LAKEHOUSE_CATALOG_PREFIX, OPS_LAKEHOUSE_CATALOG_TOKEN, OPS_LAKEHOUSE_NAMESPACE_PREFIX
func FromEnv(getenv func(string) string, log *slog.Logger, client *http.Client) (Executors, []string, error) {
	logA := LogAdapter{Log: log}
	ex := Executors{Realm: logA, Databases: logA, Kafka: SharedTopicKafka{}, Lakehouse: logA}
	var missing []string

	if u := getenv("OPS_KEYCLOAK_URL"); u != "" {
		if getenv("OPS_KEYCLOAK_CLIENT_ID") == "" || getenv("OPS_KEYCLOAK_CLIENT_SECRET") == "" {
			return ex, nil, fmt.Errorf("OPS_KEYCLOAK_URL is set: OPS_KEYCLOAK_CLIENT_ID and OPS_KEYCLOAK_CLIENT_SECRET are required")
		}
		ex.Realm = Keycloak{BaseURL: u, AdminRealm: getenv("OPS_KEYCLOAK_ADMIN_REALM"), ClientID: getenv("OPS_KEYCLOAK_CLIENT_ID"), ClientSecret: getenv("OPS_KEYCLOAK_CLIENT_SECRET"),
			RealmPrefix: getenv("OPS_KEYCLOAK_REALM_PREFIX"), TemplatePath: getenv("OPS_KEYCLOAK_REALM_TEMPLATE"), HTTP: client}
	} else {
		missing = append(missing, "Keycloak (OPS_KEYCLOAK_URL)")
	}

	if raw := getenv("OPS_PROVISION_DATABASES"); raw != "" {
		urls, err := parseURLs("OPS_PROVISION_DATABASES", raw)
		if err != nil {
			return ex, nil, err
		}
		db := Databases{URLs: urls}
		ex.Databases = db
	} else {
		missing = append(missing, "databases (OPS_PROVISION_DATABASES)")
	}

	if u := getenv("OPS_LAKEHOUSE_CATALOG_URL"); u != "" {
		ex.Lakehouse = IcebergCatalog{BaseURL: u, Prefix: getenv("OPS_LAKEHOUSE_CATALOG_PREFIX"), Token: getenv("OPS_LAKEHOUSE_CATALOG_TOKEN"),
			NamespacePrefix: getenv("OPS_LAKEHOUSE_NAMESPACE_PREFIX"), HTTP: client}
	} else {
		missing = append(missing, "lakehouse (OPS_LAKEHOUSE_CATALOG_URL)")
	}
	return ex, missing, nil
}

// parseURLs reads {"context": "connection string"} from the value or, with a leading @, from that file (a mounted secret).
func parseURLs(name, raw string) (map[string]string, error) {
	if strings.HasPrefix(raw, "@") {
		b, err := os.ReadFile(strings.TrimPrefix(raw, "@"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		raw = string(b)
	}
	urls := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &urls); err != nil || len(urls) == 0 {
		return nil, fmt.Errorf("%s must be a JSON object of context name to connection string", name)
	}
	return urls, nil
}

// PurgerFromEnv builds the purger from OPS_PURGE_DATABASES (JSON of context to connection string, or @file; its own secret, a role
// that may delete) and OPS_PURGE_KEEP_TABLES (comma list of table names kept in addition to identity_tenants).
func PurgerFromEnv(getenv func(string) string) (Purger, error) {
	raw := getenv("OPS_PURGE_DATABASES")
	if raw == "" {
		return Purger{}, fmt.Errorf("%w: OPS_PURGE_DATABASES is not set (the purge credentials, separate from OPS_PROVISION_DATABASES)", ErrNotConfigured)
	}
	urls, err := parseURLs("OPS_PURGE_DATABASES", raw)
	if err != nil {
		return Purger{}, err
	}
	p := Purger{URLs: urls}
	for _, t := range strings.Split(getenv("OPS_PURGE_KEEP_TABLES"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			p.KeepTables = append(p.KeepTables, t)
		}
	}
	return p, nil
}
