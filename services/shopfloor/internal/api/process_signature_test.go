package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/domain"
)

// A service-token caller keeps the X-Depot-* headers of whoever called it, so a forged auth time must not let it sign.
func TestCheckSignature_ServiceCallerAndMissingAuthTimeAreRefused(t *testing.T) {
	a := New(&fakeStore{})
	in := func() *domain.NewMessage {
		return &domain.NewMessage{Source: "kiosk", SourceMessageID: "r:s", Values: []domain.Characteristic{{Name: "signed-by", Type: "text", Value: "u1"}}}
	}
	fresh := strconv.FormatInt(time.Now().Unix(), 10)
	cases := map[string]struct {
		p      tenant.Principal
		header string
		want   string
	}{
		"a service caller":          {tenant.Principal{TenantID: "t", UserID: "u1", Service: "other"}, fresh, "signed-in person"},
		"no user":                   {tenant.Principal{TenantID: "t"}, fresh, "signed-in person"},
		"a token without auth_time": {tenant.Principal{TenantID: "t", UserID: "u1"}, "", "auth_time"},
	}
	for name, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(tenant.With(httptest.NewRequest(http.MethodGet, "/", nil).Context(), c.p))
		if c.header != "" {
			r.Header.Set(tenant.HeaderAuthTime, c.header)
		}
		w := httptest.NewRecorder()
		if a.checkSignature(w, r, in()) || w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "SF-0188") || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
}
