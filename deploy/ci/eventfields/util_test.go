package main

import (
	"reflect"
	"testing"
)

func TestJoinPath(t *testing.T) {
	if got := joinPath("", "a"); got != "a" {
		t.Fatalf("joinPath empty prefix = %q", got)
	}
	if got := joinPath("lines[]", "qty"); got != "lines[].qty" {
		t.Fatalf("joinPath = %q", got)
	}
}

func TestDedupeSortsAndRemovesDuplicates(t *testing.T) {
	got := dedupe([]string{"b", "a", "b", "c", "a"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupe = %v, want %v", got, want)
	}
}
