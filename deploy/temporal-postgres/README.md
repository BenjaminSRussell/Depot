# temporal-postgres

Single-replica Postgres for Temporal. The StatefulSet `serviceName` is `temporal-postgres-rw`, the ClusterIP Service clients use. Both `serviceName` and `clusterIP` are immutable, so a headless Service for stable pod DNS needs a planned recreate of the StatefulSet (delete with `--cascade=orphan`, then sync) and of the Service; do not change them in a normal sync.

`tls.enabled` (with `tls.issuer`, a cert-manager ClusterIssuer) serves TLS only: a certificate for `temporal-postgres-rw` and a pg_hba that admits TCP only over TLS with SCRAM. Stage turns it on (`temporal-postgres-stage`), and its Temporal Application sets the SQL client's `tls.enabled` (without host verification: the Temporal chart mounts no stage CA). Off in dev.
