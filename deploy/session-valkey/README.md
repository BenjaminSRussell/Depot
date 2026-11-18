# session-valkey

Valkey session store for the portal BFFs (`customer-portal-ts`, `supplier-portal-ts`) when they run more than one replica
(ADR-0014 decision 2, D26). Portal sessions map to tokens, so the store is locked down, unlike the rate-limit counter
store in `deploy/envoy-gateway`:

- **Auth:** the default user is off. Each client signs in as its own ACL user, limited to its key prefix and to the
  commands its store sends (`values.yaml` `commands`). Passwords come from the ClusterSecretStore (`<prefix>/session-valkey/<client>-password`);
  each client gets a Secret `<client>-session-store` in its namespace with `password` and its `rediss://` URL. The ACL file
  is mounted from a Secret, never passed on the command line, and holds only SHA-256 hashes of the passwords (`#<hex>`).
- **TLS only:** `--port 0` closes the plain port; the server presents a cert-manager certificate from `tls.issuer`.
- **No persistence:** `--save ""`, `--appendonly no`, an emptyDir and no volume claim. A restart signs everyone out.
- **NetworkPolicy:** ingress only from the listed clients' pods on the TLS port, no egress.

`test_session_valkey.py` renders the chart with the stage Application's values (`deploy/envs/stage/apps/session-valkey.yaml`)
and checks all four (`make -C deploy test-argocd`). Stage and prod only; dev portals keep one replica with sessions in memory.

Not wired yet: the portal charts pass `SESSION_STORE_URL` and the password but cannot mount the store's CA as
`SESSION_STORE_CA_FILE`, which `rediss://` with a private CA needs. Until a `sessionStore` CA value exists in those charts,
stage keeps both portals at one replica without the store (`deploy/scripts/stage_apps.py` SINGLETONS).
