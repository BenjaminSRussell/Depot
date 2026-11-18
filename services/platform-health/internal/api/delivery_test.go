package api_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const repoRoot = "../../../.."

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDeliveryFilesCarryRequiredContent(t *testing.T) {
	tests := []struct {
		file string
		want []string
	}{
		{"services/platform-health/Dockerfile", []string{"USER 65532:65532"}},
		{"deploy/Makefile", []string{"deploy-local:", "tofu", "k3d image import", "root-local.yaml"}},
		{"deploy/tofu/cluster/main.tf", []string{"precondition", `helm_release" "argocd"`, `helm_release" "cert_manager"`}},
		{"services/platform-health/NOT-IMPLEMENTED.md", []string{"Deliverable 1", "slice 2 of DPT-1", "Root `make deploy-local`"}},
		{"services/platform-health/charts/hello-ledger/templates/deployment.yaml", []string{"secretKeyRef", "/healthz", "/readyz"}},
		{".github/workflows/gate.yml", []string{"gate:", "if: always()", "dorny/paths-filter"}},
		{".github/workflows/ci.yml", []string{"cosign sign", "cosign attest", "anchore/sbom-action", "anchore/scan-action", "aquasecurity/trivy-action"}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			body := readRepoFile(t, tc.file)
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("%s missing %q", tc.file, w)
				}
			}
		})
	}
}

func TestEnvOverlaysArePinnedByDigestAndHoldNoSecrets(t *testing.T) {
	err := filepath.WalkDir(filepath.Join(repoRoot, "deploy/envs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(b), "tag:") {
			t.Errorf("%s contains %q: overlays pin digests and hold no secret values", path, "tag:")
		}
		// A connection URL is allowed only when its password is an ExternalSecret template placeholder
		// (eam-database, transport-db); a literal DSN would carry the secret.
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "postgres://") && !strings.Contains(line, "{{ .password }}@") {
				t.Errorf("%s contains a literal postgres:// URL: overlays hold no secret values", path)
			}
		}
		if hasLiteralPassword(string(b)) {
			t.Errorf("%s contains a literal password value: overlays hold no secret values", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Stage (FR-1.3, FR-1.4, B-09 part b): every stage Application reads this repository at the release tag of
// deploy/envs/stage/release.yaml, never a branch, and pins its image to a signed digest or, until one is recorded, to the
// release version tag. No file under deploy/envs/stage carries the all-zero placeholder digest.
func TestStageApplicationsArePinnedToTheRelease(t *testing.T) {
	release := readRepoFile(t, "deploy/envs/stage/release.yaml")
	m := regexp.MustCompile(`(?m)^revision: v(\d+\.\d+\.\d+)\s*$`).FindStringSubmatch(release)
	if m == nil {
		t.Fatal("deploy/envs/stage/release.yaml: revision is not a release tag vX.Y.Z")
	}
	revision, version := "v"+m[1], m[1]
	zero := "sha256:" + strings.Repeat("0", 64)
	revisionLine := regexp.MustCompile(`^\s*targetRevision:\s*["']?([^"'\s]+)`)
	semver := regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	tagParam := regexp.MustCompile(`(?m)- name: ["']?image\.tag["']?\n\s+value: ["']?([^"'\n]+?)["']?\s*$`)
	repoSources := 0
	err := filepath.WalkDir(filepath.Join(repoRoot, "deploy/envs/stage"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(b)
		if strings.Contains(body, zero) {
			t.Errorf("%s pins the all-zero placeholder digest", path)
		}
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			rm := revisionLine.FindStringSubmatch(line)
			if rm == nil {
				continue
			}
			if !semver.MatchString(rm[1]) {
				t.Errorf("%s:%d: targetRevision %q is not a pinned version (never main or a branch)", path, i+1, rm[1])
			}
			// A source of this repository reads the release tag; its repoURL is within two lines.
			near := strings.Join(lines[max(0, i-2):min(len(lines), i+3)], "\n")
			if strings.Contains(near, "github.com/BenjaminSRussell/Depot.git") {
				repoSources++
				if rm[1] != revision {
					t.Errorf("%s:%d: this repository at %q, want the release tag %s", path, i+1, rm[1], revision)
				}
			}
		}
		for _, tm := range tagParam.FindAllStringSubmatch(body, -1) {
			if tm[1] != version && tm[1] != "{{ .image }}" {
				t.Errorf("%s: image.tag %q is not the release version %s", path, tm[1], version)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if repoSources < 20 {
		t.Errorf("found %d stage sources of this repository; the scan no longer sees the Applications", repoSources)
	}
}

func hasLiteralPassword(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		_, value, found := strings.Cut(line, "password:")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if strings.HasPrefix(value, "{{") && strings.HasSuffix(value, "}}") {
			continue
		}
		return true
	}
	return false
}

func TestHasLiteralPassword(t *testing.T) {
	if hasLiteralPassword(`password: "{{ .password }}"`) {
		t.Error("pure template placeholder must be allowed")
	}
	if !hasLiteralPassword(`password: hunter2`) {
		t.Error("literal password must be rejected")
	}
}
