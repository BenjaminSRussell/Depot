package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunStopsOnMalformedServicePublicKeys(t *testing.T) {
	err := run(context.Background(), func(k string) string {
		return map[string]string{"DEPOT_SERVICE_NAME": "transport", "DEPOT_SERVICE_PUBLIC_KEYS": "workflow-ts=zzz"}[k]
	})
	if err == nil || !strings.Contains(err.Error(), "DEPOT_SERVICE_PUBLIC_KEYS") {
		t.Fatalf("run with a malformed key = %v", err)
	}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run without DATABASE_URL = %v", err)
	}
}

func TestOptimizerURLPrefersTheNewNameAndWarnsOnTheOld(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := optimizerURL(env(map[string]string{"ROUTING_PY_URL": "http://a", "TMS_OPTIMIZER_URL": "http://b"}), log); got != "http://a" || buf.Len() != 0 {
		t.Fatalf("new name: %q %q", got, buf.String())
	}
	if got := optimizerURL(env(map[string]string{"TMS_OPTIMIZER_URL": "http://b"}), log); got != "http://b" || !strings.Contains(buf.String(), "deprecated") {
		t.Fatalf("old name: %q %q", got, buf.String())
	}
	if got := optimizerURL(env(nil), log); got != "" {
		t.Fatalf("unset: %q", got)
	}
}
