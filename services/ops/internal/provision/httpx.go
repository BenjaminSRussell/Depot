package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// tenantIDPattern is what a tenant id may contain before it becomes part of a realm name, a namespace or a URL path. Identity issues
// UUIDs; anything else is refused so a crafted id can never escape the path it is placed in.
var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

func checkTenantID(id string) error {
	if !tenantIDPattern.MatchString(id) {
		return fmt.Errorf("tenant id %q is not a valid identifier (letters, digits and hyphens only)", id)
	}
	return nil
}

func httpClientOr(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// doJSON sends a request with an optional JSON body and returns the status and (truncated) response body. Transport errors are
// returned; HTTP statuses are the caller's to judge.
func doJSON(ctx context.Context, c *http.Client, method, url, bearer string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClientOr(c).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
