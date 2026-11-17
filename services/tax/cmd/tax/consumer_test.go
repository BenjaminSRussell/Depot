package main

import (
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/tax/internal/feed"
)

// The Intrastat consumer starts only when the service principal table gives it the feed duty, and the platform table does; it reads
// fulfillment's packing slip topic and receiving's receipt topic under its own group.
func TestIntrastatSeeded(t *testing.T) {
	if intrastatSeeded(map[string][]string{}) || intrastatSeeded(map[string][]string{feed.ConsumerName: {"other.duty"}}) {
		t.Fatal("unseeded consumer reported seeded")
	}
	if !intrastatSeeded(tenant.ServicePrincipalTable()) {
		t.Fatal("platform table does not seed service:" + feed.ConsumerName)
	}
	cfg, err := intrastatKafkaConfig(func(k string) string { return map[string]string{"KAFKA_BROKERS": "kafka:9092"}[k] })
	if err != nil || cfg.Group != feed.ConsumerName || len(cfg.Topics) != 2 || cfg.Topics[0] != feed.PackingSlipTopic || cfg.Topics[1] != feed.ReceiptTopic {
		t.Fatalf("kafka config %+v %v", cfg, err)
	}
	if _, err := intrastatKafkaConfig(func(string) string { return "" }); err == nil {
		t.Fatal("kafka config without KAFKA_BROKERS accepted")
	}
}

func TestConsumeRefusesWithoutADatabase(t *testing.T) {
	if err := consume(t.Context(), func(string) string { return "" }); err == nil {
		t.Fatal("consume without DATABASE_URL started")
	}
}
