package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

func TestPromSourceReadsTheThreeQueries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if !strings.Contains(q, "[6h]") {
			t.Errorf("window not in query: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "histogram_quantile"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"job":"ledger"},"value":[1,"0.25"]},{"metric":{"job":"quiet"},"value":[1,"NaN"]}]}}`))
		case strings.Contains(q, `status=~"5.."`):
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"job":"ledger"},"value":[1,"3"]}]}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"job":"ledger"},"value":[1,"1000"]},{"metric":{"job":"quiet"},"value":[1,"0"]}]}}`))
		}
	}))
	defer srv.Close()
	samples, err := PromSource{URL: srv.URL}.Samples(context.Background(), 6*time.Hour)
	if err != nil || len(samples) != 2 {
		t.Fatalf("samples = %+v err=%v", samples, err)
	}
	for _, s := range samples {
		switch s.Service {
		case "ledger":
			if s.Requests != 1000 || s.Failed != 3 || s.P95 == nil || *s.P95 != 0.25 {
				t.Fatalf("ledger = %+v", s)
			}
		case "quiet":
			if s.P95 != nil {
				t.Fatalf("NaN p95 kept: %+v", s)
			}
		}
	}
	if promRange(30*24*time.Hour) != "30d" || promRange(time.Hour) != "1h" {
		t.Fatal("range selectors")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","error":"parse error"}`))
	}))
	defer bad.Close()
	if _, err := (PromSource{URL: bad.URL}).Samples(context.Background(), time.Hour); err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("error answer = %v", err)
	}
}

func TestPromSourceReadsOperationsByJobAndRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if !strings.Contains(q, "by (job, route") {
			t.Errorf("not grouped by route: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		series := func(v string) string {
			return `{"status":"success","data":{"result":[{"metric":{"job":"ledger","route":"/v1/ledger/journal-entries"},"value":[1,"` + v + `"]},{"metric":{"job":"ledger","route":"/v1/ledger/other"},"value":[1,"99"]},{"metric":{"job":"ledger"},"value":[1,"7"]}]}}`
		}
		switch {
		case strings.Contains(q, "histogram_quantile"):
			_, _ = w.Write([]byte(series("0.3")))
		case strings.Contains(q, `status=~"5.."`):
			_, _ = w.Write([]byte(series("4")))
		default:
			_, _ = w.Write([]byte(series("2000")))
		}
	}))
	defer srv.Close()
	ops := []domain.OperationSLO{{Name: "post-entry", Service: "ledger", Route: "/v1/ledger/journal-entries"}, {Name: "gone", Service: "ledger", Route: "/v1/ledger/gone"}}
	got, err := PromSource{URL: srv.URL}.OperationSamples(context.Background(), time.Hour, ops)
	if err != nil || len(got) != 2 {
		t.Fatalf("samples = %+v err=%v", got, err)
	}
	if got[0].Requests != 2000 || got[0].Failed != 4 || got[0].P95 == nil || *got[0].P95 != 0.3 {
		t.Fatalf("post-entry = %+v", got[0])
	}
	if got[1].Requests != 0 || got[1].P95 != nil {
		t.Fatalf("a route that never appears has no traffic: %+v", got[1])
	}
}

func TestSLOReportCarriesOperationLines(t *testing.T) {
	e := newEnv3(t, time.Now().UTC(), func(c *Config) {
		c.SLOSource = fakeOps{}
		c.SLOTargets.Operations = []domain.OperationSLO{{Name: "post-entry", Service: "ledger", Route: "/v1/ledger/journal-entries", LatencyP95Ms: 100}}
	})
	rec := e.do("GET", "/v1/ops/slo?window=1h", "alice", DutyView, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("slo = %d %s", rec.Code, rec.Body)
	}
	var rep domain.SLOReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Services) != 1 || len(rep.Operations) != 1 || !rep.Operations[0].LatencyBreach || !rep.Operations[0].Breached || rep.Breached != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

type fakeOps struct{}

func (fakeOps) Samples(context.Context, time.Duration) ([]domain.SLOSample, error) {
	p := 0.05
	return []domain.SLOSample{{Service: "ledger", Requests: 1000, P95: &p}}, nil
}

func (fakeOps) OperationSamples(_ context.Context, _ time.Duration, ops []domain.OperationSLO) ([]domain.OperationSample, error) {
	p := 0.25 // 250 ms against the operation's own 100 ms
	return []domain.OperationSample{{Operation: ops[0], Requests: 500, P95: &p}}, nil
}
