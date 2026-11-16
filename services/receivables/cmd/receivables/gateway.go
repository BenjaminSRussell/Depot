package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/api"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway/stripe"
)

var (
	stripeAccountID = regexp.MustCompile(`^acct_[A-Za-z0-9]{1,58}$`)
	bankRef         = regexp.MustCompile(`^[A-Z0-9]{1,40}$`)
)

// gatewayFromEnv reads the card and ACH payment provider (#30). RECEIVABLES_PAYMENT_PROVIDER empty leaves payments off; "stripe"
// needs STRIPE_SECRET_KEY and STRIPE_WEBHOOK_SECRET (each from the chart's Secret, as a value or a _FILE path; the webhook secret
// may list several, comma separated: the platform endpoint, the Connect endpoint, or two while one is rolled) and takes
// STRIPE_PUBLISHABLE_KEY (plain config, required: pk_...), RECEIVABLES_STRIPE_ACCOUNTS (a JSON object of legal entity id to Stripe Connect account
// id), RECEIVABLES_GATEWAY_BANK_REF (the ar_bank_account account_ref of the Stripe clearing account, default STRIPE) and
// STRIPE_API_BASE (tests only). Secrets never appear in an error.
func gatewayFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (*api.GatewayConfig, error) {
	provider := strings.ToLower(strings.TrimSpace(getenv("RECEIVABLES_PAYMENT_PROVIDER")))
	switch provider {
	case "":
		return nil, nil
	case "stripe":
	default:
		return nil, fmt.Errorf("RECEIVABLES_PAYMENT_PROVIDER %q is not supported (use stripe, or leave it empty)", provider)
	}
	secret, err := secretFromEnv(getenv, readFile, "STRIPE_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	hooks, err := secretFromEnv(getenv, readFile, "STRIPE_WEBHOOK_SECRET")
	if err != nil {
		return nil, err
	}
	gw, err := stripe.New(stripe.Config{SecretKey: secret, PublishableKey: strings.TrimSpace(getenv("STRIPE_PUBLISHABLE_KEY")),
		WebhookSecrets: strings.Split(hooks, ","), BaseURL: getenv("STRIPE_API_BASE")})
	if err != nil {
		return nil, err
	}
	accounts := map[string]string{}
	if raw := strings.TrimSpace(getenv("RECEIVABLES_STRIPE_ACCOUNTS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &accounts); err != nil {
			return nil, errors.New("RECEIVABLES_STRIPE_ACCOUNTS must be a JSON object of legal entity id to Stripe account id (acct_...)")
		}
	}
	normalized := make(map[string]string, len(accounts))
	for le, acct := range accounts {
		if !ids.Valid(le) || !stripeAccountID.MatchString(acct) {
			return nil, fmt.Errorf("RECEIVABLES_STRIPE_ACCOUNTS entry %q needs a UUID legal entity id and an acct_ account id", le)
		}
		normalized[strings.ToLower(le)] = acct
	}
	ref := strings.TrimSpace(getenv("RECEIVABLES_GATEWAY_BANK_REF"))
	if ref == "" {
		ref = "STRIPE"
	}
	if !bankRef.MatchString(ref) {
		return nil, errors.New("RECEIVABLES_GATEWAY_BANK_REF must be 1 to 40 upper-case letters and digits (an ar_bank_account account_ref)")
	}
	return &api.GatewayConfig{Gateway: gw, Accounts: normalized, BankAccountRef: ref}, nil
}

// secretFromEnv reads NAME, or the file NAME_FILE names; the value is never echoed.
func secretFromEnv(getenv func(string) string, readFile func(string) ([]byte, error), name string) (string, error) {
	if path := getenv(name + "_FILE"); path != "" {
		raw, err := readFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE could not be read", name)
		}
		if v := strings.TrimSpace(string(raw)); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("%s_FILE is empty", name)
	}
	if v := strings.TrimSpace(getenv(name)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("RECEIVABLES_PAYMENT_PROVIDER=stripe needs %s (or %s_FILE)", name, name)
}
