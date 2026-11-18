package identity

import (
	"os"
	"strings"
	"testing"
)

// B-04: the ext_authz OPA loads its decision-log mask and readiness rule as local policies under data.system
// (deploy/gateway/authz-opa.yaml). OPA refuses local data under a bundle root, and a bundle root over data.system would
// let the bundle replace the mask, so no root may be or contain system. authztest uses the same roots for its bundle.
func TestBundleRootsLeaveSystemToTheLocalPolicies(t *testing.T) {
	for _, root := range bundleRoots {
		if root == "" || root == "system" || strings.HasPrefix(root, "system/") {
			t.Fatalf("bundle root %q claims data.system", root)
		}
	}
	src, err := os.ReadFile("authztest/decisionlog_test.go")
	if err != nil {
		t.Fatal(err)
	}
	want := `var bundleRoots = []string{"` + strings.Join(bundleRoots, `", "`) + `"}`
	if !strings.Contains(string(src), want) {
		t.Fatalf("authztest's bundle roots differ from identity's: want %s", want)
	}
}
