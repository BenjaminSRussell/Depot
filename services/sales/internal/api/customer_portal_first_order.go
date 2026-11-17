package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/sales/internal/domain"
	"github.com/BenjaminSRussell/depot/services/sales/internal/store"
	"github.com/BenjaminSRussell/depot/services/sales/internal/upstream"
)

const (
	// portalPricingChannel is the sales channel the portal prices under: pricing's procedure assignment for it chooses the procedure.
	portalPricingChannel = "PORTAL"
	codePortalPricing    = "SAL-CUS-004"
	// codePortalPricingRefused: pricing answered 401 or 403 to sales' service token (not retryable).
	codePortalPricingRefused = "SAL-CUS-005"
	maxListingsPerLine       = 500
)

// PortalPricer prices a customer's lines with pricing's customer price resolution (negotiated condition records, discounts and the
// procedure assigned to the channel). Sales holds no price list of its own.
type PortalPricer interface {
	ResolvePrices(ctx context.Context, req upstream.PriceRequest) ([]upstream.PricedLine, error)
}

// WithPortalPricer sets the price source for portal lines of products the customer has not bought before. Without it such a line is
// refused as before (422 SAL-CUS-002): only a reorder at the last price is possible.
func (a *API) WithPortalPricer(p PortalPricer) *API {
	a.pricer = p
	return a
}

// portalFirstOrderLines fills the slots of the lines whose product the customer has not ordered before (fresh): each product must be
// explicitly listed for the customer or its group (a customer ALLOW or a group ALLOW in force on the order date, no DENY ahead of it;
// BR-27.13), the unit must be named, and the price is pricing's resolution for this customer, unit and quantity: the net unit price
// with no further discount. Site, warehouse, currency and invoice account are those of the customer's own history (the line
// before it in this order, else its most recent approved order), so a portal order never carries one the customer typed. Problems of a
// line are added to fields; a service that cannot answer is returned.
func (a *API) portalFirstOrderLines(r *http.Request, customer string, p tenant.Principal, req *portalOrderRequest, in *createOrderRequest,
	slots []*orderLineRequest, fresh []int, fields map[string]string) error {
	ctx := r.Context()
	site, warehouse := "", ""
	for _, sl := range slots {
		if sl != nil {
			site, warehouse = sl.SiteID, sl.WarehouseID
			break
		}
	}
	if in.Currency == "" {
		// Nothing in this order is a reorder: take the terms of the customer's most recent approved order.
		recent, _, err := a.portalHistory(r, customer, nil)
		if err != nil {
			return err
		}
		if len(recent) == 0 {
			for _, i := range fresh {
				fields[fmt.Sprintf("lines[%d].productId", i)] = "cannot be ordered online until you have a first order with us; ask your sales contact"
			}
			return nil
		}
		in.Currency, in.InvoiceAccountID, site, warehouse = recent[0].Currency, recent[0].InvoiceAccountID, recent[0].SiteID, recent[0].WarehouseID
	}
	group, err := a.resolveGroup(ctx, p.LegalEntityID, customer, nil)
	if err != nil {
		return err
	}
	day := a.now().UTC().Truncate(24 * time.Hour)
	var priceLines []upstream.PriceLine
	var at []int
	for _, i := range fresh {
		l := req.Lines[i]
		field := fmt.Sprintf("lines[%d]", i)
		q, qerr := decimal.NewFromString(l.Quantity)
		switch {
		case qerr != nil || !q.IsPositive():
			fields[field+".quantity"] = "must be a positive number"
			continue
		case strings.TrimSpace(l.Unit) == "":
			fields[field+".unit"] = "is required for a product you have not ordered before"
			continue
		}
		listed, err := a.portalProductListed(ctx, p.LegalEntityID, customer, *group, l.ProductID, day)
		if err != nil {
			return err
		}
		if !listed {
			fields[field+".productId"] = "is not a product listed for you; ask your sales contact to add it"
			continue
		}
		priceLines = append(priceLines, upstream.PriceLine{ProductID: l.ProductID, Unit: strings.TrimSpace(l.Unit), Quantity: l.Quantity})
		at = append(at, i)
	}
	if len(priceLines) == 0 {
		return nil
	}
	priced, err := a.pricer.ResolvePrices(ctx, upstream.PriceRequest{LegalEntityID: p.LegalEntityID, CustomerID: customer, CustomerGroupID: *group,
		Currency: in.Currency, PricingDate: req.RequestedShipDate, SalesChannel: portalPricingChannel, Lines: priceLines})
	if err != nil {
		if errors.Is(err, upstream.ErrNotConfigured) {
			return httpx.NewProblem(http.StatusServiceUnavailable, codePortalPricing, "prices cannot be read: the pricing service is not configured")
		}
		var se *upstream.StatusError
		if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
			// Pricing refuses sales' token: retrying cannot help, an operator has to fix the trust between the services.
			slog.ErrorContext(ctx, "pricing refused the sales service token", "status", se.Status)
			return httpx.NewProblem(http.StatusBadGateway, codePortalPricingRefused, "the order cannot be priced online; contact your sales contact")
		}
		return httpx.NewProblem(http.StatusServiceUnavailable, codePortalPricing, "the pricing service could not price the order; retry")
	}
	zero := "0"
	for n, i := range at {
		l, pl := req.Lines[i], priced[n]
		if pl.ErrorCode != "" || !pl.NetUnitPrice.IsPositive() {
			fields[fmt.Sprintf("lines[%d].productId", i)] = "has no price for you in " + in.Currency + "; ask your sales contact"
			continue
		}
		slots[i] = &orderLineRequest{ProductID: l.ProductID, Quantity: l.Quantity, Unit: priceLines[n].Unit, SiteID: site, WarehouseID: warehouse,
			UnitPrice: pl.NetUnitPrice.String(), DiscPct: &zero, RequestedShipDate: req.RequestedShipDate}
	}
	return nil
}

// portalProductListed reports whether a product is explicitly listed for the customer: a customer ALLOW or a group ALLOW in force on
// the day, with no DENY of a higher level (BR-27.13). A product with no listing at all is not listed here, although staff may order it
// as unrestricted: the portal offers a customer only what its listings name.
func (a *API) portalProductListed(ctx context.Context, legalEntityID, customer, group, productID string, day time.Time) (bool, error) {
	productID = strings.ToLower(productID)
	rows, err := a.store.ListListings(ctx, store.ListingFilter{LegalEntityID: legalEntityID, ProductID: productID, Limit: maxListingsPerLine})
	if err != nil {
		return false, err
	}
	entries, err := listingEntries(rows)
	if err != nil {
		return false, err
	}
	d := domain.EvaluateListing(entries, strings.ToLower(customer), group, productID, day)
	return d.Allowed && (d.Reason == domain.ReasonCustomerAllow || d.Reason == domain.ReasonGroupAllow), nil
}

func listingEntries(rows []store.ProductListing) ([]domain.ListingEntry, error) {
	entries := make([]domain.ListingEntry, 0, len(rows))
	for _, l := range rows {
		e := domain.ListingEntry{ScopeType: l.ScopeType, ScopeKey: l.ScopeKey, ProductID: strings.ToLower(l.ProductID), ListingType: l.ListingType}
		var err error
		if e.ValidFrom, err = optionalDay(l.ValidFrom); err != nil {
			return nil, err
		}
		if e.ValidTo, err = optionalDay(l.ValidTo); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// portalListedProducts lists the products the customer's own and its group's listings allow on the day and that are not in skip (the
// products it has bought, which the list carries with their last price): at most limit, newest listing first. A product a customer or
// group DENY covers, or one whose ALLOW applies to another scope only, is not offered.
func (a *API) portalListedProducts(ctx context.Context, legalEntityID, customer string, skip map[string]bool, limit int) ([]string, error) {
	group, err := a.resolveGroup(ctx, legalEntityID, customer, nil)
	if err != nil {
		return nil, err
	}
	var rows []store.ProductListing
	for _, key := range []string{strings.ToLower(customer), *group} {
		got, err := a.store.ListListings(ctx, store.ListingFilter{LegalEntityID: legalEntityID, ScopeKey: key, Limit: maxListingsPerLine})
		if err != nil {
			return nil, err
		}
		rows = append(rows, got...)
	}
	entries, err := listingEntries(rows)
	if err != nil {
		return nil, err
	}
	day := a.now().UTC().Truncate(24 * time.Hour)
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		if e.ListingType != domain.ListingAllow || skip[e.ProductID] || seen[e.ProductID] {
			continue
		}
		seen[e.ProductID] = true
		if d := domain.EvaluateListing(entries, strings.ToLower(customer), *group, e.ProductID, day); d.Allowed && (d.Reason == domain.ReasonCustomerAllow || d.Reason == domain.ReasonGroupAllow) {
			out = append(out, e.ProductID)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
