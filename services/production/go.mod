module github.com/BenjaminSRussell/depot/services/production

go 1.26.0

require (
	github.com/BenjaminSRussell/depot/platform v0.0.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/jackc/pgx/v5 v5.13.0
	github.com/shopspring/decimal v1.4.0
	pgregory.net/rapid v1.3.0
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/twmb/franz-go v1.22.3 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.15.1 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/text v0.44.0 // indirect
)

replace github.com/BenjaminSRussell/depot/platform => ../../platform/go
