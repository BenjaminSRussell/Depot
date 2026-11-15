// Package customsdata reads, at the time of a goods movement, what Intrastat needs of its partner and its products (#13): the country the
// partner's VAT registration is in (the party service, duty party.tax-registration.read) and each product's commodity code, country of
// origin and net weight (the product service, duty product.customs.read). Fulfillment puts it on packingslip.posted and receiving on
// receipt.posted so the tax service can map EU cross-border movements without a call back. A read that fails leaves its part out and is
// reported, so the shipment or receipt itself is never held up by a master data outage.
package customsdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	timeout         = 3 * time.Second
	maxResponseSize = 1 << 20
	maxIDs          = 200

	partyAudience   = "party"
	productAudience = "product"
	dutyParty       = "party.tax-registration.read"
	dutyProduct     = "product.customs.read"
)

// Partner is a customer's or vendor's tax registration.
type Partner struct {
	Country string
	VATID   string
}

// Product is a product's customs attributes; a part that is not set is empty.
type Product struct {
	CommodityCode string
	OriginCountry string
	NetWeightKg   string
}

// Result is what could be read: Partner is nil when the partner has no registration or the read failed, and a product missing from
// Products has no customs attributes or was not read.
type Result struct {
	Partner  *Partner
	Products map[string]Product
}

// Source reads the customs data of a movement. A Result is returned even with an error: it holds what the other read gave.
type Source interface {
	Lookup(ctx context.Context, legalEntityID, partnerID string, productIDs []string) (Result, error)
}

// Client is the HTTP Source over the party and product services.
type Client struct {
	partyURL, productURL string
	signer               *tenant.ServiceSigner
	http                 *http.Client
}

// New returns a client; both URLs and the signer are required.
func New(partyURL, productURL string, signer *tenant.ServiceSigner) (*Client, error) {
	if partyURL == "" || productURL == "" {
		return nil, errors.New("customsdata: PARTY_URL and PRODUCT_URL are required")
	}
	if signer == nil {
		return nil, errors.New("customsdata: a service signing key is required")
	}
	return &Client{partyURL: strings.TrimRight(partyURL, "/"), productURL: strings.TrimRight(productURL, "/"), signer: signer,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// FromEnv builds the client from PARTY_URL, PRODUCT_URL and the service signing key. It returns nil, nil when any is missing: the events
// then carry no customs data and the tax service cannot map the movement.
func FromEnv(getenv func(string) string) (*Client, error) {
	party, product := getenv("PARTY_URL"), getenv("PRODUCT_URL")
	if party == "" || product == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil || signer == nil {
		return nil, err
	}
	return New(party, product, signer)
}

// Lookup reads the partner's registration and the products' customs attributes. The calls are signed with only the one duty each needs,
// for the legal entity of the movement.
func (c *Client) Lookup(ctx context.Context, legalEntityID, partnerID string, productIDs []string) (Result, error) {
	out := Result{Products: map[string]Product{}}
	var errs []error
	if partnerID != "" {
		var body struct {
			Items []struct {
				PartnerID string `json:"partnerId"`
				Country   string `json:"country"`
				VATID     string `json:"vatId"`
			} `json:"items"`
		}
		if err := c.get(ctx, c.partyURL+"/v1/party/partners:tax-registrations?"+url.Values{"ids": {partnerID}}.Encode(), partyAudience, dutyParty, legalEntityID, &body); err != nil {
			errs = append(errs, fmt.Errorf("partner tax registration: %w", err))
		} else if len(body.Items) > 0 {
			out.Partner = &Partner{Country: body.Items[0].Country, VATID: body.Items[0].VATID}
		}
	}
	seen := map[string]bool{}
	var wanted []string
	for _, id := range productIDs {
		if id = strings.ToLower(id); id != "" && !seen[id] {
			seen[id] = true
			wanted = append(wanted, id)
		}
	}
	for start := 0; start < len(wanted); start += maxIDs {
		end := min(start+maxIDs, len(wanted))
		var body struct {
			Items []struct {
				ID            string `json:"id"`
				CommodityCode string `json:"commodityCode"`
				OriginCountry string `json:"originCountry"`
				NetWeightKg   string `json:"netWeightKg"`
			} `json:"items"`
		}
		target := c.productURL + "/v1/product/products:customs?" + url.Values{"ids": {strings.Join(wanted[start:end], ",")}}.Encode()
		if err := c.get(ctx, target, productAudience, dutyProduct, legalEntityID, &body); err != nil {
			errs = append(errs, fmt.Errorf("product customs data: %w", err))
			break
		}
		for _, it := range body.Items {
			out.Products[strings.ToLower(it.ID)] = Product{CommodityCode: it.CommodityCode, OriginCountry: it.OriginCountry, NetWeightKg: it.NetWeightKg}
		}
	}
	return out, errors.Join(errs...)
}

func (c *Client) get(ctx context.Context, target, audience, duty, legalEntityID string, out any) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if _, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{duty}); err != nil {
		return fmt.Errorf("sign %s request: %w", audience, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s request: %w", audience, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("read %s response: %w", audience, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", audience, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w", audience, err)
	}
	return nil
}
