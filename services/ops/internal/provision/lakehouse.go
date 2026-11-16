package provision

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// IcebergCatalog is the LakehouseAdmin over an Iceberg REST catalog (POST /v1/namespaces, DELETE /v1/namespaces/{ns}). The tenant's
// namespace is NamespacePrefix + the tenant id with hyphens as underscores (default "tenant_"). Prefix is the catalog's optional
// route prefix (/v1/{prefix}/namespaces). A namespace that exists is a success on create, one that is absent a success on drop; a
// namespace that still holds tables is not dropped (the catalog answers 409): the tables are the tenant's data.
type IcebergCatalog struct {
	BaseURL         string
	Prefix          string
	Token           string
	NamespacePrefix string
	HTTP            *http.Client
}

// Namespace is the catalog namespace of a tenant.
func (c IcebergCatalog) Namespace(t Tenant) (string, error) {
	if err := checkTenantID(t.ID); err != nil {
		return "", err
	}
	return orDefault(c.NamespacePrefix, "tenant_") + strings.ReplaceAll(t.ID, "-", "_"), nil
}

func (c IcebergCatalog) route() string {
	r := strings.TrimSuffix(c.BaseURL, "/") + "/v1/"
	if c.Prefix != "" {
		r += url.PathEscape(c.Prefix) + "/"
	}
	return r + "namespaces"
}

func (c IcebergCatalog) EnsureNamespace(ctx context.Context, t Tenant, dry bool) (string, error) {
	ns, err := c.Namespace(t)
	if err != nil {
		return "", err
	}
	if dry {
		return fmt.Sprintf("would create Iceberg namespace %s at %s unless it exists", ns, c.route()), nil
	}
	body := map[string]any{"namespace": []string{ns}, "properties": map[string]string{"passage.tenant_id": t.ID, "passage.region": t.Region}}
	st, b, err := doJSON(ctx, c.HTTP, http.MethodPost, c.route(), c.Token, body)
	if err != nil {
		return "", fmt.Errorf("lakehouse catalog: %w", err)
	}
	switch st {
	case http.StatusOK, http.StatusCreated:
		return "created Iceberg namespace " + ns, nil
	case http.StatusConflict:
		return "Iceberg namespace " + ns + " already exists", nil
	}
	return "", fmt.Errorf("lakehouse catalog: create namespace %s: status %d %s", ns, st, snippet(b))
}

func (c IcebergCatalog) DropNamespace(ctx context.Context, t Tenant, dry bool) (string, error) {
	ns, err := c.Namespace(t)
	if err != nil {
		return "", err
	}
	if dry {
		return "would drop Iceberg namespace " + ns + " (absent is a success; one that still has tables is refused)", nil
	}
	st, b, err := doJSON(ctx, c.HTTP, http.MethodDelete, c.route()+"/"+url.PathEscape(ns), c.Token, nil)
	if err != nil {
		return "", fmt.Errorf("lakehouse catalog: %w", err)
	}
	switch st {
	case http.StatusOK, http.StatusNoContent:
		return "dropped Iceberg namespace " + ns, nil
	case http.StatusNotFound:
		return "Iceberg namespace " + ns + " was already absent", nil
	case http.StatusConflict:
		return "", fmt.Errorf("lakehouse catalog: namespace %s is not empty; export and drop its tables first: %s", ns, snippet(b))
	}
	return "", fmt.Errorf("lakehouse catalog: drop namespace %s: status %d %s", ns, st, snippet(b))
}
