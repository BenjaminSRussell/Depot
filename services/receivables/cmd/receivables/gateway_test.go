package main

import (
	"errors"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func noFile(string) ([]byte, error) { return nil, errors.New("no file") }

func TestGatewayFromEnv(t *testing.T) {
	if gw, err := gatewayFromEnv(envOf(nil), noFile); gw != nil || err != nil {
		t.Fatalf("no provider: %v %v", gw, err)
	}
	base := map[string]string{"RECEIVABLES_PAYMENT_PROVIDER": "stripe", "STRIPE_SECRET_KEY": "sk_test_abc", "STRIPE_WEBHOOK_SECRET": "whsec_a,whsec_b",
		"STRIPE_PUBLISHABLE_KEY": "pk_test_abc", "RECEIVABLES_STRIPE_ACCOUNTS": `{"00000000-0000-0000-0000-0000000000A1":"acct_1Abc"}`}
	gw, err := gatewayFromEnv(envOf(base), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if gw.BankAccountRef != "STRIPE" || gw.Accounts["00000000-0000-0000-0000-0000000000a1"] != "acct_1Abc" || gw.Gateway.PublishableKey() != "pk_test_abc" {
		t.Fatalf("config = %+v", gw)
	}
	files := func(path string) ([]byte, error) {
		if path == "/s/key" {
			return []byte("sk_test_fromfile\n"), nil
		}
		return []byte("whsec_file\n"), nil
	}
	fromFiles := map[string]string{"RECEIVABLES_PAYMENT_PROVIDER": "stripe", "STRIPE_SECRET_KEY_FILE": "/s/key", "STRIPE_WEBHOOK_SECRET_FILE": "/s/hook"}
	if _, err := gatewayFromEnv(envOf(fromFiles), files); err != nil {
		t.Fatalf("files: %v", err)
	}
	for name, change := range map[string]map[string]string{
		"unknown provider": {"RECEIVABLES_PAYMENT_PROVIDER": "paypal"},
		"no secret":        {"STRIPE_SECRET_KEY": ""},
		"no webhook":       {"STRIPE_WEBHOOK_SECRET": ""},
		"bad account":      {"RECEIVABLES_STRIPE_ACCOUNTS": `{"le":"acct_1"}`},
		"bad bank ref":     {"RECEIVABLES_GATEWAY_BANK_REF": "stripe clearing"},
		"publishable key":  {"STRIPE_SECRET_KEY": "pk_live_oops"},
	} {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range change {
			env[k] = v
		}
		_, err := gatewayFromEnv(envOf(env), noFile)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "sk_test_abc") || strings.Contains(err.Error(), "whsec_") || strings.Contains(err.Error(), "pk_live_oops") {
			t.Errorf("%s: the error shows a secret: %v", name, err)
		}
	}
}
