package store_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	kindPostingTx   = "posting_tx"
	repoRoot        = "../../../.."
	inventoryFile   = "fiscal_numbers.json"
	numberingFolder = "services/numbering"
)

var allocationPattern = regexp.MustCompile(`next_sequence|last_number|next_number|_counter\b|lpad\(|numbering/sequences`)

var skippedDirs = map[string]bool{"node_modules": true, "testdata": true, "tests": true, "dist": true, "vendor": true, ".venv": true}

var scannedExtensions = map[string]bool{".go": true, ".sql": true, ".ts": true, ".py": true}

type inventoryEntry struct {
	Files  []string `json:"files"`
	Kind   string   `json:"kind"`
	Fiscal bool     `json:"fiscal"`
	Note   string   `json:"note"`
}

func scanAllocationSites(t *testing.T) []string {
	t.Helper()
	var hits []string
	root := filepath.Join(repoRoot, "services")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repoRoot, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skippedDirs[d.Name()] || rel == numberingFolder {
				return filepath.SkipDir
			}
			return nil
		}
		if !scannedExtensions[filepath.Ext(path)] || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if allocationPattern.Match(body) {
			hits = append(hits, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(hits)
	return hits
}

func inventoryProblems(hits []string, inventory []inventoryEntry) []string {
	listed := map[string]bool{}
	var problems []string
	for _, entry := range inventory {
		if entry.Fiscal && entry.Kind != kindPostingTx {
			problems = append(problems, "fiscal number allocated outside a posting transaction: "+strings.Join(entry.Files, ", "))
		}
		for _, file := range entry.Files {
			listed[file] = true
		}
	}
	found := map[string]bool{}
	for _, hit := range hits {
		found[hit] = true
		if !listed[hit] {
			problems = append(problems, "unclassified number allocation site "+hit+": add it to "+inventoryFile+" as posting_tx, remote_non_fiscal or not_a_document_number")
		}
	}
	for file := range listed {
		if !found[file] {
			problems = append(problems, "stale inventory entry "+file+": no allocation pattern matches it any more")
		}
	}
	sort.Strings(problems)
	return problems
}

func TestFiscalNumberInventory(t *testing.T) {
	raw, err := os.ReadFile(inventoryFile)
	if err != nil {
		t.Fatal(err)
	}
	var inventory []inventoryEntry
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	for _, p := range inventoryProblems(scanAllocationSites(t), inventory) {
		t.Errorf("%s (pattern %s)", p, allocationPattern)
	}
}

func TestFiscalNumberInventoryCatchesDrift(t *testing.T) {
	inventory := []inventoryEntry{
		{Files: []string{"services/a/store.go"}, Kind: kindPostingTx, Fiscal: true},
		{Files: []string{"services/gone/store.go"}, Kind: "not_a_document_number"},
		{Files: []string{"services/b/client.go"}, Kind: "remote_non_fiscal", Fiscal: true},
	}
	got := strings.Join(inventoryProblems([]string{"services/a/store.go", "services/new/store.go"}, inventory), "\n")
	for _, want := range []string{"unclassified number allocation site services/new/store.go", "stale inventory entry services/gone/store.go", "outside a posting transaction: services/b/client.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems %q missing %q", got, want)
		}
	}
}
