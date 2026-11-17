"""`python -m prediction.migrate`: applies the migrations as the owner role and exits, the chart's PreSync migration Job (D28).

The same contract as dbx.RunMigrateCommand in the Go services:
- the owner role's URL is MIGRATE_DATABASE_URL, else DATABASE_URL;
- each attempt (connect, app-role check, store.migrate under the dbx advisory lock) is retried with back-off from 1 s to
  30 s for MIGRATE_RETRY_TIMEOUT (a Go duration: `90s`, `5m`, `1h30m`, `1.5s`, `0`; default 5 minutes) when the database
  cannot be reached or drops the session, each retry logged;
- the app role (MIGRATE_APP_ROLE, default depot_app) is refused, either way round (see APP_ROLE_QUERY), unless
  MIGRATE_ALLOW_APP_ROLE=true;
- a migration's SQL error, a rejected login, a missing database or a TLS failure fails at once.

A failed connection carries no SQLSTATE in libpq, so is_connection_error asks the server instead (PQping): a server that
answers and accepts connections rejected this login or database, which no retry fixes; one that does not answer, or is
starting up or shutting down, is retried. TLS failures are recognised by libpq's own (client-side) messages.
Serving pods keep MIGRATE_ON_START=false and connect as depot_app."""

from __future__ import annotations

import json
import os
import re
import sys
import time
from typing import Callable, Mapping

APP_ROLE = "depot_app"
# True when the session is tied to the app role either way round: current_user or session_user is it, the current or session
# role is a member of it (superusers left out), or it is a member of current_user and so inherits the owner's rights.
# The same query as dbx.AppRoleQuery in the Go services.
APP_ROLE_QUERY = """select session_user::text = %(app)s or current_user::text = %(app)s or (app.oid is not null and (
    (not cur.rolsuper and pg_has_role(cur.oid, app.oid, 'MEMBER'))
    or (not sess.rolsuper and pg_has_role(sess.oid, app.oid, 'MEMBER'))
    or pg_has_role(app.oid, cur.oid, 'MEMBER'))), current_user::text, session_user::text
from pg_roles cur
join pg_roles sess on sess.rolname = session_user
left join pg_roles app on app.rolname = %(app)s
where cur.rolname = current_user"""
_UNITS = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3, "s": 1.0, "m": 60.0, "h": 3600.0}
_DURATION_PART = re.compile(r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h)")
# libpq's client-side TLS failures: no retry fixes them.
_TLS = re.compile(r"SSL error|certificate verify failed|server certificate .* does not match|does not support SSL|"
                  r"root certificate file|SSL connection has been closed unexpectedly", re.I)


def migrate_url(env: Mapping[str, str]) -> str:
    url = env.get("MIGRATE_DATABASE_URL") or env.get("DATABASE_URL") or ""
    if not url:
        raise ValueError("MIGRATE_DATABASE_URL or DATABASE_URL is required to migrate")
    return url


def retry_seconds(env: Mapping[str, str]) -> float:
    """MIGRATE_RETRY_TIMEOUT as a Go duration (as dbx.MigrateRetry parses it), else 300 s."""
    raw = env.get("MIGRATE_RETRY_TIMEOUT", "")
    if raw == "":
        return 300.0
    if raw == "0":
        return 0.0
    total, at = 0.0, 0
    for m in _DURATION_PART.finditer(raw):
        if m.start() != at:
            break
        total += float(m.group(1)) * _UNITS[m.group(2)]
        at = m.end()
    if at == 0 or at != len(raw):
        raise ValueError(f"MIGRATE_RETRY_TIMEOUT must be a Go duration such as 90s, 5m or 1h30m, got {raw!r}")
    return total


def is_tls_error(err: BaseException) -> bool:
    return bool(_TLS.search(str(err)))


def is_connection_error(err: BaseException, url: str) -> bool:
    """A database that could not be reached or dropped the session (retried), not a statement's own error, a rejected login,
    a missing database or a TLS failure."""
    import psycopg
    from psycopg import pq
    from psycopg.conninfo import make_conninfo

    state = getattr(err, "sqlstate", None) or ""
    if state:
        return state.startswith("08") or state in ("57P01", "57P02", "57P03", "53300")
    if not isinstance(err, psycopg.OperationalError) or is_tls_error(err):
        return False
    # No SQLSTATE: a failed connection or a lost session. Ask the server: OK means it answers and accepts connections, so
    # this login or database was rejected; REJECT (starting up, shutting down) and NO_RESPONSE are worth a retry.
    return pq.PGconn.ping(make_conninfo(url).encode()) in (pq.Ping.REJECT, pq.Ping.NO_RESPONSE)


def log(message: str, **fields: object) -> None:
    print(json.dumps({"level": "warn", "message": message, "service": "prediction", **fields}), file=sys.stderr)


def _attempt(url: str, app_role: str | None) -> None:
    import psycopg

    from prediction.store import migrate

    if app_role is not None:
        with psycopg.connect(url, connect_timeout=10) as conn:
            row = conn.execute(APP_ROLE_QUERY, {"app": app_role}).fetchone()
        if row and row[0]:
            raise PermissionError(
                f"refusing to migrate as {row[1]!r} (session {row[2]!r}): it is the serving role {app_role}, a member of it, "
                f"or a role {app_role} inherits. Use the owner role's URL (MIGRATE_DATABASE_URL), or set "
                "MIGRATE_ALLOW_APP_ROLE=true where it owns the schema (dev only)")
    migrate(url)


def run(env: Mapping[str, str], sleep: Callable[[float], None] = time.sleep, warn: Callable[..., None] = log) -> None:
    import psycopg

    url = migrate_url(env)
    deadline = time.monotonic() + retry_seconds(env)
    app_role = None if env.get("MIGRATE_ALLOW_APP_ROLE") == "true" else (env.get("MIGRATE_APP_ROLE") or APP_ROLE)
    wait = 1.0
    attempt = 0
    while True:
        attempt += 1
        try:
            _attempt(url, app_role)
            return
        except psycopg.Error as err:
            if not is_connection_error(err, url) or time.monotonic() + wait > deadline:
                raise
            warn("migration database not reachable; retrying", attempt=attempt, retryInSeconds=wait, error=str(err))
            sleep(wait)
            wait = min(2 * wait, 30.0)


def main(env: Mapping[str, str] = os.environ) -> int:
    try:
        run(env)
    except Exception as err:  # noqa: BLE001 - any failure fails the Job
        print(json.dumps({"level": "error", "message": "migrations failed", "service": "prediction", "error": str(err)}), file=sys.stderr)
        return 1
    print(json.dumps({"level": "info", "message": "migrations applied", "service": "prediction"}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
