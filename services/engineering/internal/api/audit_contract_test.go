package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/engineering/internal/api"
	"github.com/BenjaminSRussell/depot/services/engineering/internal/domain"
)

// auditFixture is the audit service's contract fixture for a signature record: the audit service's test posts it
// with an engineering-signed token and checks how it is stored (B-06, AUD-API-1). This test keeps it equal to the
// body engineering's client really sends, so the two cannot drift.
const auditFixture = "../../../audit/api/testdata/engineering-signature-change.json"

func TestHTTPAuditorBodyMatchesTheAuditContractFixture(t *testing.T) {
	var got map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(hs.Close)
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("engineering", key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "carol", LegalEntityID: "00000000-0000-0000-0000-0000000000e1"})
	sig := domain.Signature{ID: "s1", RecordID: "d1", DocumentNumber: "DRW-1", DocumentVersion: "02", Transition: "released", SignerID: "carol", PrintedName: "Carol Kane", Meaning: "Approved for release", OriginalsHash: "abc123", SignedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)}
	if err := api.NewHTTPAuditor(hs.URL, signer).RecordSignature(ctx, sig); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(auditFixture)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("audit body\n%v\ndiffers from the audit contract fixture\n%v", got, want)
	}
}
