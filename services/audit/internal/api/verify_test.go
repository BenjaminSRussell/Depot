package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

func TestVerifyChangesRoute(t *testing.T) {
	broken := store.ChangeChain{Verified: false, Entries: 4, FromSeq: 1, ToSeq: 4, Breaks: 1,
		FirstBreak: &domain.Break{Seq: 3, ID: "e3", Kind: domain.BreakHash}, KnownBreaks: store.KnownBreaks{LegacyPrecision: 1}}
	for _, tc := range []struct {
		name, path, duties string
		store              *fakeStore
		status             int
		want               string
		bounds             [2]int64
	}{
		{"whole chain", "/v1/audit/records:verify", "audit.verify", &fakeStore{chain: store.ChangeChain{Verified: true, Entries: 2}}, http.StatusOK, `"verified":true`, [2]int64{}},
		{"range with a break", "/v1/audit/records:verify?fromSeq=1&toSeq=4", "audit.verify", &fakeStore{chain: broken}, http.StatusOK, `"firstBreak":{"seq":3,"id":"e3","kind":"hash-mismatch"}`, [2]int64{1, 4}},
		{"read duty does not verify", "/v1/audit/records:verify", "audit.read", &fakeStore{}, http.StatusForbidden, "AUTH-0003", [2]int64{}},
		{"bad fromSeq", "/v1/audit/records:verify?fromSeq=0", "audit.verify", &fakeStore{}, http.StatusBadRequest, domain.CodeVerifyRange, [2]int64{}},
		{"reversed range", "/v1/audit/records:verify?fromSeq=5&toSeq=2", "audit.verify", &fakeStore{}, http.StatusBadRequest, domain.CodeVerifyRange, [2]int64{}},
		{"store error", "/v1/audit/records:verify", "audit.verify", &fakeStore{err: errors.New("down")}, http.StatusInternalServerError, "COM-0500", [2]int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, http.MethodGet, tc.path, "", tc.duties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				if tc.store.verifyRange != tc.bounds {
					t.Fatalf("range %v, want %v", tc.store.verifyRange, tc.bounds)
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				for _, k := range []string{"verified", "entries", "fromSeq", "toSeq", "head", "breaks", "knownBreaks", "anchors"} {
					if _, ok := body[k]; !ok {
						t.Errorf("verdict has no %s: %s", k, rec.Body.String())
					}
				}
			}
		})
	}
}
