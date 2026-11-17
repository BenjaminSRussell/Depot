package tracectx_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/tracectx"
)

const (
	validHeader = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	validTrace  = "4bf92f3577b34da6a3ce929d0e0e4736"
)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		ok           bool
	}{
		{"valid", validHeader, true},
		{"padded", "  " + validHeader + " ", true},
		{"empty", "", false},
		{"uppercase", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01", false},
		{"short trace id", "00-4bf92f3577b34da6a3ce929d0e0e47-00f067aa0ba902b7-01", false},
		{"all-zero trace id", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", false},
		{"all-zero span id", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", false},
		{"extra segment", validHeader + "-00", false},
		{"not hex", "00-zzf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			span, ok := tracectx.Parse(tc.header)
			if ok != tc.ok || (ok && span.TraceID != validTrace) {
				t.Fatalf("Parse(%q) = %+v, %v", tc.header, span, ok)
			}
		})
	}
}

func serve(t *testing.T, header string) tracectx.Span {
	t.Helper()
	var got tracectx.Span
	h := tracectx.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ok bool
		if got, ok = tracectx.From(r.Context()); !ok {
			t.Fatal("no span in context")
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	if header != "" {
		req.Header.Set(tracectx.Header, header)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestMiddlewareContinuesAValidTraceWithANewSpan(t *testing.T) {
	span := serve(t, validHeader)
	if span.TraceID != validTrace || span.SpanID == "00f067aa0ba902b7" || span.Flags != "01" {
		t.Fatalf("span %+v", span)
	}
}

func TestMiddlewareMintsARootForAbsentAndMalformedHeaders(t *testing.T) {
	for _, header := range []string{"", "garbage", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"} {
		span := serve(t, header)
		if !hex32.MatchString(span.TraceID) || span.TraceID == "00000000000000000000000000000000" || len(span.SpanID) != 16 {
			t.Fatalf("header %q gave span %+v", header, span)
		}
	}
}

func TestTraceIDFallsBackToTheRequestID(t *testing.T) {
	var got string
	h := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = tracectx.TraceID(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "req-9")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "req-9" {
		t.Fatalf("trace id %q", got)
	}
}

func TestInjectSetsAChildOfTheContextSpanAndNothingWithout(t *testing.T) {
	bare := httptest.NewRequest(http.MethodGet, "/", nil)
	tracectx.Inject(bare)
	if bare.Header.Get(tracectx.Header) != "" {
		t.Fatalf("header set without a span: %v", bare.Header)
	}
	root, _ := tracectx.Parse(validHeader)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tracectx.With(bare.Context(), root))
	tracectx.Inject(req)
	child, ok := tracectx.Parse(req.Header.Get(tracectx.Header))
	if !ok || child.TraceID != validTrace || child.SpanID == root.SpanID {
		t.Fatalf("injected %q", req.Header.Get(tracectx.Header))
	}
}
