package tracectx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	Header       = "traceparent"
	sampledFlags = "01"
)

var traceparentRe = regexp.MustCompile(`^[0-9a-f]{2}-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

type Span struct {
	TraceID string
	SpanID  string
	Flags   string
}

type key struct{}

func Parse(header string) (Span, bool) {
	m := traceparentRe.FindStringSubmatch(strings.TrimSpace(header))
	if m == nil || strings.Trim(m[1], "0") == "" || strings.Trim(m[2], "0") == "" {
		return Span{}, false
	}
	return Span{TraceID: m[1], SpanID: m[2], Flags: m[3]}, true
}

func (s Span) Traceparent() string {
	return "00-" + s.TraceID + "-" + s.SpanID + "-" + s.Flags
}

func (s Span) Child() Span {
	return Span{TraceID: s.TraceID, SpanID: randomHex(8), Flags: s.Flags}
}

func NewRoot() Span {
	return Span{TraceID: randomHex(16), SpanID: randomHex(8), Flags: sampledFlags}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func With(ctx context.Context, s Span) context.Context {
	return context.WithValue(ctx, key{}, s)
}

func From(ctx context.Context) (Span, bool) {
	s, ok := ctx.Value(key{}).(Span)
	return s, ok
}

func TraceID(ctx context.Context) string {
	if s, ok := From(ctx); ok {
		return s.TraceID
	}
	return middleware.GetReqID(ctx)
}

func Inject(req *http.Request) {
	if s, ok := From(req.Context()); ok {
		req.Header.Set(Header, s.Child().Traceparent())
	}
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := NewRoot()
		if parent, ok := Parse(r.Header.Get(Header)); ok {
			span = parent.Child()
		}
		next.ServeHTTP(w, r.WithContext(With(r.Context(), span)))
	})
}
