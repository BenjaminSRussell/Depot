# depot-server

The single Windows binary of Depot (ADR-0004, issue #607). It supervises the service executables in `<install>\bin` as
child processes on loopback ports, runs the built-in gateway (JWT verification, OPA decision, path routing, the embedded web
app) and runs as the `Depot` Windows service.

```
depot-server serve      # run in the foreground (console) or, under the Service Control Manager, as the service
depot-server migrate    # start every service once with MIGRATE_ON_START=true, wait until ready, stop
depot-server doctor     # dependency checks; -json for machine output; exit 1 on any FAIL
depot-server services   # the children serve would start (start command, ports, readiness path) as JSON
depot-server version
```

Configuration: `%ProgramData%\Depot\depot.yaml` (or `DEPOT_CONFIG`, `DEPOT_DATA_DIR` off Windows), `conf.d\*.yaml`, then registry
values (Windows). The property and key names are in [docs/ops/windows-install.md](../../docs/ops/windows-install.md).

A `services:` entry (in `depot.yaml` or a `conf.d` file) takes `name`, `exe`, `args`, `env`, `route`, `feature`, `optional`, `noDb`,
`disabled`, and for children that read no `HTTP_ADDR`: `{port}` / `{adminPort}` in `args` and `env` values, `singlePort: true` (API and
readiness on one port) and `readyPath` (default `/readyz`). `{install}` and `{data}` expand everywhere.

Build (Linux or Windows):

```
cd apps/web && npm ci && npm run build
cp -r apps/web/dist tools/depot-server/internal/web/dist        # not committed
cd tools/depot-server
GOOS=windows GOARCH=amd64 go build -tags webdist -ldflags "-s -w -X main.version=1.0.0" -o depot-server.exe .
GOFIPS140=v1.0.0 ...                                                # FIPS variant (set requireFips in depot.yaml)
```

Without `-tags webdist` the gateway serves the API only (the build and tests need no web build). Windows-only code
(`service_windows.go`, `policy_windows.go`, `job_windows.go`) sits behind build tags, so Linux builds and tests run without
`golang.org/x/sys/windows`. Tests use fakes for the process runner, Postgres, HTTP and network dials;
`TestPgInspectorAgainstPostgres` uses `DEPOT_TEST_DATABASE_URL` when set.


## Identity provider requirements

Electronic signatures on the shop floor (PI steps, #25) need the access token to carry `auth_time`; the built-in gateway forwards it as `X-Depot-Auth-Time`. Keycloak issues it by default and honours `prompt=login&max_age=0` for the kiosk's "Sign in again". An IdP whose access tokens have no `auth_time` can sign in and use everything else, but every signature is refused (SF-0188) until it does. See `services/shopfloor/README.md`.
