// Package logpseudo pseudonymizes personal identifiers in log records before they are exported to Loki (issue 605).
//
// Microsoft pseudonymizes the personal data in its own system logs inside the EU Data Boundary; Depot owes the same
// for its logs and traces. Handler wraps a slog.Handler. An attribute whose key names a person (user, email, name,
// person, subject, ip, ...), and any email address inside any string value or the message, is replaced by a stable
// pseudonym: the HMAC of the value under the tenant's MAC key (fieldcrypt.Keys.MAC derivation), so the same value maps
// to the same pseudonym inside one tenant and to a different one in another, and the pseudonym cannot be reversed
// without the key. When no key is configured, or the tenant is not known, the value is replaced by a fixed marker and
// never passed through.
//
// The key (KeyFromEnv) is the dedicated pseudonymization key DEPOT_LOG_PSEUDONYM_KEY (or the file named by
// DEPOT_LOG_PSEUDONYM_KEY_FILE), base64 of 32 bytes, used only for this MAC; without it, fieldcrypt's current master key
// (FIELDCRYPT_KEYS). Both go through the same derivation, so a dedicated key set to the bytes of fieldcrypt's current key
// gives the same pseudonyms. A service that holds no field-encryption keys can pseudonymize with the dedicated key alone.
//
// The tenant comes from the request context (tenant.FromContext) when the call carries one (slog.InfoContext), else
// from a tenant attribute on the record (tenant, tenantId, tenant_id). httpx.Serve calls Install, so a service needs
// nothing else.
//
// Subject gives the same pseudonym for one value outside the handler, whatever the profile says: the authorization
// decision record (platform/go/authz) carries it, never the raw user id (LoadSubjectKey, B-04). SubjectAttr marks the
// attribute so the handler does not pseudonymize it a second time; no string value is exempt.
//
// Limits: a person's name in free text (a message, a field not named like a person) cannot be recognised and is not
// changed; log a name under a key such as name, displayName or user instead. A value that is a struct or map is
// checked as text (fmt %+v) for email addresses only.
package logpseudo

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/BenjaminSRussell/depot/platform/cloudprofile"
	"github.com/BenjaminSRussell/depot/platform/fieldcrypt"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// Env turns pseudonymization on when "true" (it is always on under the eu-data-boundary overlay).
const Env = "LOG_PSEUDONYMIZE"

// Redacted replaces a value that could not be pseudonymized (no key, or no tenant).
const Redacted = "[redacted]"

// EmailRedacted replaces an email address that could not be pseudonymized.
const EmailRedacted = "[redacted-email]"

// Prefix starts every pseudonym.
const Prefix = "pn_"

// The dedicated pseudonymization key (B-04): base64 of 32 bytes in DEPOT_LOG_PSEUDONYM_KEY, or in the file named by
// DEPOT_LOG_PSEUDONYM_KEY_FILE. The Envoy ext_authz OPA reads the same variable (policies/authz, log_pseudonym).
const (
	EnvKey     = "DEPOT_LOG_PSEUDONYM_KEY"
	EnvKeyFile = "DEPOT_LOG_PSEUDONYM_KEY_FILE"
)

// keyID names the dedicated key inside the fieldcrypt.Keys that carries it; it never leaves the process.
const keyID = "log-pseudonym"

// macDomain keeps the product's former name so pseudonyms in retained logs still match (docs/adr/0005-rename-to-depot.md).
const macDomain = "passage.log.pseudonym.v1\x00"

var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)

// MACer is fieldcrypt.Keys' MAC.
type MACer interface {
	MAC(tenantID string, message []byte) ([]byte, error)
}

// Enabled reports whether logs are pseudonymized: always under the eu-data-boundary overlay of the cloud profile,
// and when LOG_PSEUDONYMIZE=true.
func Enabled(getenv func(string) string) (bool, error) {
	p, err := cloudprofile.FromEnv(getenv)
	if err != nil {
		return false, err
	}
	for _, o := range p.Overlays {
		if o == "eu-data-boundary" {
			return true, nil
		}
	}
	return strings.EqualFold(strings.TrimSpace(getenv(Env)), "true"), nil
}

// KeyFromEnv is the MAC logs are pseudonymized under: the dedicated key (EnvKey, EnvKeyFile) when one is set, else
// fieldcrypt's current master key (FIELDCRYPT_KEYS, FIELDCRYPT_KEYS_FILE), else nil (every personal value Redacted). A key
// that is set but cannot be read is an error, never a silent fall back to the other key.
func KeyFromEnv(getenv func(string) string) (MACer, error) {
	raw := strings.TrimSpace(getenv(EnvKey))
	if file := strings.TrimSpace(getenv(EnvKeyFile)); file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // the path is the operator's configuration
		if err != nil {
			return nil, fmt.Errorf("logpseudo: read %s: %w", EnvKeyFile, err)
		}
		raw = strings.TrimSpace(string(b))
	}
	if raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("logpseudo: %s is not base64", EnvKey)
		}
		keys, err := fieldcrypt.NewKeys(keyID, map[string][]byte{keyID: key})
		if err != nil {
			return nil, fmt.Errorf("logpseudo: %s must be base64 of 32 bytes", EnvKey)
		}
		return keys, nil
	}
	keys, err := fieldcrypt.FromEnv(getenv)
	if err != nil || keys == nil {
		return nil, err
	}
	return keys, nil
}

// Install wraps slog's default handler when pseudonymization is enabled (see Enabled) and reports whether it did. The
// key is KeyFromEnv's; with none configured every personal value is replaced by Redacted. A bad profile or key
// configuration is an error: logs are never left raw because the configuration could not be read.
func Install(getenv func(string) string) (bool, error) {
	on, err := Enabled(getenv)
	if err != nil || !on {
		return false, err
	}
	mac, err := KeyFromEnv(getenv)
	if err != nil {
		return false, fmt.Errorf("logpseudo: %w", err)
	}
	subjectKey.CompareAndSwap(nil, &subjectHolder{mac: mac})
	return InstallWith(mac), nil
}

// subjectKey is the MAC Subject pseudonymizes under (a *subjectHolder; nil until LoadSubjectKey or SetSubjectKey).
var subjectKey atomic.Pointer[subjectHolder]

type subjectHolder struct{ mac MACer }

// SetSubjectKey sets the key Subject pseudonymizes under, whether or not whole-log pseudonymization is on. A nil mac
// makes Subject return Redacted for every value.
func SetSubjectKey(mac MACer) { subjectKey.Store(&subjectHolder{mac: mac}) }

// LoadSubjectKey sets Subject's key from KeyFromEnv (the dedicated key, else fieldcrypt's) once per process; a later
// call is a no-op. Unlike Install it runs whatever the profile says, because the authorization decision
// record (platform/go/authz) always carries a pseudonym, never a raw user id. A configuration that cannot be read leaves
// the key unset, so every subject is Redacted, and is returned for the caller to log.
func LoadSubjectKey(getenv func(string) string) error {
	if subjectKey.Load() != nil {
		return nil
	}
	mac, err := KeyFromEnv(getenv)
	if err != nil {
		subjectKey.CompareAndSwap(nil, &subjectHolder{})
		return fmt.Errorf("logpseudo: subject key: %w", err)
	}
	subjectKey.CompareAndSwap(nil, &subjectHolder{mac: mac})
	return nil
}

// Subject is the pseudonym of a user identifier inside a tenant under the key LoadSubjectKey or SetSubjectKey set: the
// same value Handler writes for a personal attribute, so a record that carries it can be matched with other pseudonymized
// logs of the tenant. Without a key, or without a tenant, it is Redacted; an empty value stays empty. It never returns
// the value itself.
func Subject(tenantID, value string) string {
	if value == "" {
		return ""
	}
	var mac MACer
	if h := subjectKey.Load(); h != nil {
		mac = h.mac
	}
	if p, ok := (scrubber{mac: mac, tenantID: tenantID}).pseudonym(value); ok {
		return p
	}
	return Redacted
}

// SubjectAttr is the attribute key=Subject(tenantID, value), marked as already pseudonymized: Handler writes it as it is
// instead of pseudonymizing the pseudonym a second time. Only a value built here carries the mark; a string that merely
// looks like a pseudonym is pseudonymized like any other personal value, so no one can choose the pseudonym their own
// log lines carry.
func SubjectAttr(key, tenantID, value string) slog.Attr {
	return slog.Any(key, pseudonymized(Subject(tenantID, value)))
}

// pseudonymized is a value Subject produced (a pseudonym, Redacted or empty). Outside Handler it logs as the string.
type pseudonymized string

func (p pseudonymized) LogValue() slog.Value { return slog.StringValue(string(p)) }

// installMu makes InstallWith's check and set one step: a service that runs several httpx.Serve listeners installs from
// each of them concurrently, and two unsynchronised installs would wrap the handler twice.
var installMu sync.Mutex

// InstallWith wraps slog's default handler unconditionally, unless it is already wrapped. A nil mac redacts. It is safe
// for concurrent use.
func InstallWith(mac MACer) bool {
	installMu.Lock()
	defer installMu.Unlock()
	if _, done := slog.Default().Handler().(*Handler); done {
		return false
	}
	slog.SetDefault(slog.New(New(slog.Default().Handler(), mac)))
	return true
}

// part is one WithAttrs or WithGroup call, kept so the attributes are rewritten when the record, and with it the
// tenant, is known.
type part struct {
	group string
	attrs []slog.Attr
}

// Handler is a slog.Handler that pseudonymizes personal data before it reaches next.
type Handler struct {
	next  slog.Handler
	mac   MACer
	parts []part
}

// New wraps next. A nil mac replaces personal values with Redacted.
func New(next slog.Handler, mac MACer) *Handler { return &Handler{next: next, mac: mac} }

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.parts = append(append([]part(nil), h.parts...), part{attrs: append([]slog.Attr(nil), attrs...)})
	return &c
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.parts = append(append([]part(nil), h.parts...), part{group: name})
	return &c
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	for i := len(h.parts) - 1; i >= 0; i-- {
		if p := h.parts[i]; p.group != "" {
			attrs = []slog.Attr{{Key: p.group, Value: slog.GroupValue(attrs...)}}
		} else {
			attrs = append(append([]slog.Attr(nil), p.attrs...), attrs...)
		}
	}
	tenantID := ""
	if p, ok := tenant.FromContext(ctx); ok {
		tenantID = p.TenantID
	}
	if tenantID == "" {
		tenantID = findTenant(attrs)
	}
	s := scrubber{mac: h.mac, tenantID: tenantID}
	out := slog.NewRecord(r.Time, r.Level, s.text(r.Message), r.PC)
	for _, a := range attrs {
		out.AddAttrs(s.attr(a, false))
	}
	return h.next.Handle(ctx, out)
}

type scrubber struct {
	mac      MACer
	tenantID string
}

// PseudonymFor is the pseudonym of value for a tenant under mac, as logged. The TypeScript and Python loggers compute the
// same value; their tests hold known answers taken from this function.
func PseudonymFor(mac MACer, tenantID, value string) string {
	p, _ := scrubber{mac: mac, tenantID: tenantID}.pseudonym(value)
	return p
}

func (s scrubber) pseudonym(value string) (string, bool) {
	if s.mac == nil || s.tenantID == "" {
		return "", false
	}
	sum, err := s.mac.MAC(s.tenantID, []byte(macDomain+strings.ToLower(strings.TrimSpace(value))))
	if err != nil {
		return "", false
	}
	return Prefix + hex.EncodeToString(sum[:8]), true
}

// text replaces every email address in text by its pseudonym (or EmailRedacted).
func (s scrubber) text(text string) string {
	if !strings.Contains(text, "@") {
		return text
	}
	return emailRE.ReplaceAllStringFunc(text, func(m string) string {
		if p, ok := s.pseudonym(m); ok {
			return p
		}
		return EmailRedacted
	})
}

// attr rewrites one attribute. Everything inside a group whose own key names a person (a "user" or "person" group) is
// personal data, whatever its keys are.
func (s scrubber) attr(a slog.Attr, inPersonal bool) slog.Attr {
	if p, ok := a.Value.Any().(pseudonymized); ok && a.Value.Kind() == slog.KindLogValuer {
		return slog.String(a.Key, string(p))
	}
	v := a.Value.Resolve()
	inPersonal = inPersonal || personal(a.Key)
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		out := make([]any, 0, len(group))
		for _, g := range group {
			out = append(out, s.attr(g, inPersonal))
		}
		return slog.Group(a.Key, out...)
	}
	if inPersonal {
		if v.Kind() == slog.KindString && (v.String() == "" || v.String() == Redacted) {
			return a
		}
		str := fmt.Sprint(v.Any())
		if p, ok := s.pseudonym(str); ok {
			return slog.String(a.Key, p)
		}
		return slog.String(a.Key, Redacted)
	}
	switch v.Kind() {
	case slog.KindString:
		if t := s.text(v.String()); t != v.String() {
			return slog.String(a.Key, t)
		}
	case slog.KindAny:
		if str := fmt.Sprintf("%+v", v.Any()); strings.Contains(str, "@") {
			if t := s.text(str); t != str {
				return slog.String(a.Key, t)
			}
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func findTenant(attrs []slog.Attr) string {
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			if id := findTenant(v.Group()); id != "" {
				return id
			}
			continue
		}
		switch strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(a.Key)) {
		case "tenant", "tenantid":
			if v.Kind() == slog.KindString {
				return v.String()
			}
		}
	}
	return ""
}

var (
	personalWords = map[string]bool{
		"user": true, "username": true, "userid": true, "email": true, "mail": true, "person": true, "subject": true, "sub": true,
		"ip": true, "phone": true, "actor": true, "principal": true, "approver": true, "requester": true, "caller": true,
		"employee": true, "contact": true, "firstname": true, "lastname": true, "fullname": true, "displayname": true,
		"givenname": true, "surname": true, "upn": true,
	}
	nameQualifiers = map[string]bool{
		"first": true, "last": true, "full": true, "display": true, "given": true, "family": true, "person": true,
		"user": true, "customer": true, "employee": true, "contact": true, "author": true,
	}
)

// personal reports whether an attribute key names a person: user, email, name, person, subject, ip and the like,
// in snake_case, kebab-case, dotted or camelCase.
func personal(key string) bool {
	words := splitWords(key)
	hasName := false
	for _, w := range words {
		if personalWords[w] {
			return true
		}
		if w == "name" {
			hasName = true
		}
	}
	if !hasName {
		return false
	}
	if len(words) == 1 {
		return true
	}
	for _, w := range words {
		if nameQualifiers[w] {
			return true
		}
	}
	return false
}

func splitWords(key string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}
