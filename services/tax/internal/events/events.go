package events

const Source = "/depot/tax/tax"

const ProviderFallback = "dev.depot.tax.provider.fallback.v1"

type ProviderFallbackData struct {
	LegalEntityID string `json:"legalEntityId"`
	RecommitID    string `json:"recommitId"`
	Provider      string `json:"provider"`
	Reason        string `json:"reason"`
}
