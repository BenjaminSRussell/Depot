module github.com/BenjaminSRussell/depot/tools/depot-server

go 1.26.0

require (
	github.com/BenjaminSRussell/depot/platform v0.0.0
	github.com/BenjaminSRussell/depot/services/identity v0.0.0
	github.com/jackc/pgx/v5 v5.12.0
	golang.org/x/sys v0.50.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/go-chi/chi/v5 v5.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/BenjaminSRussell/depot/platform => ../../platform/go

replace github.com/BenjaminSRussell/depot/services/identity => ../../services/identity
