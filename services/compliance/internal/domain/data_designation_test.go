package domain

import "testing"

func TestEffective_MostRestrictiveWins(t *testing.T) {
	for name, tc := range map[string]struct {
		tenant, legalEntity, record bool
		class, source               string
	}{
		"nothing designated":      {false, false, false, ClassNONE, SourceDefault},
		"tenant only":             {true, false, false, ClassCUI, SourceTenant},
		"legal entity only":       {false, true, false, ClassCUI, SourceLegalEntity},
		"record only":             {false, false, true, ClassCUI, SourceRecord},
		"tenant and legal entity": {true, true, false, ClassCUI, SourceLegalEntity},
		"legal entity and record": {false, true, true, ClassCUI, SourceRecord},
		"every scope":             {true, true, true, ClassCUI, SourceRecord},
		"tenant and record":       {true, false, true, ClassCUI, SourceRecord},
	} {
		class, source := Effective(tc.tenant, tc.legalEntity, tc.record)
		if class != tc.class || source != tc.source {
			t.Errorf("%s: got %s/%s, want %s/%s", name, class, source, tc.class, tc.source)
		}
	}
}

func TestParseEntityType(t *testing.T) {
	for in, want := range map[string]bool{"project": true, " Project ": true, "": false, "contract": false, "party": false, "item": false, "attachment": false} {
		if _, ok := ParseEntityType(in); ok != want {
			t.Errorf("%q: got %v, want %v", in, ok, want)
		}
	}
	if got, _ := ParseEntityType(" Project "); got != EntityProject {
		t.Errorf("normalised: %q", got)
	}
}
