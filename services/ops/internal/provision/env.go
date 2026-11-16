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
//	Databases: OPS_PROVISION_DATABASES (JSON {"ledger": "postgres://..."} or @file); optional OPS_PROVISION_DB_PURGE=true,
//	           OPS_PROVISION_DB_KEEP_TABLES (comma list, default identity_tenants)
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
		if strings.HasPrefix(raw, "@") {
			b, err := os.ReadFile(strings.TrimPrefix(raw, "@"))
			if err != nil {
				return ex, nil, fmt.Errorf("OPS_PROVISION_DATABASES: %w", err)
			}
			raw = string(b)
		}
		urls := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &urls); err != nil || len(urls) == 0 {
			return ex, nil, fmt.Errorf("OPS_PROVISION_DATABASES must be a JSON object of context name to connection string")
		}
		db := Databases{URLs: urls, PurgeEnabled: getenv("OPS_PROVISION_DB_PURGE") == "true"}
		if k := getenv("OPS_PROVISION_DB_KEEP_TABLES"); k != "" {
			db.KeepTables = []string{}
			for _, t := range strings.Split(k, ",") {
				if t = strings.TrimSpace(t); t != "" {
					db.KeepTables = append(db.KeepTables, t)
				}
			}
		}
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
