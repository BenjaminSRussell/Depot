"""`python -m prediction.migrate`: applies the migrations as the owner role and exits, the chart's PreSync migration Job (D28).

The same contract as dbx.RunMigrateCommand in the Go services: the owner role's URL is MIGRATE_DATABASE_URL, else
DATABASE_URL; an unreachable database is retried with back-off for MIGRATE_RETRY_TIMEOUT (`90s`, `5m`, `0`; default 5
minutes), each attempt logged; depot_app, or a member of it, is refused unless MIGRATE_ALLOW_APP_ROLE=true; the files run
under the dbx advisory lock (store.migrate). A migration's SQL error fails at once. Serving pods keep MIGRATE_ON_START=false
and connect as depot_app."""

from __future__ import annotations

import json
import os
import re
import sys
import time
from typing import Callable, Mapping

APP_ROLE = "depot_app"
APP_ROLE_QUERY = """select r.rolname = 'depot_app' or (not r.rolsuper
  and exists (select 1 from pg_roles where rolname = 'depot_app')
  and pg_has_role(r.oid, 'depot_app', 'MEMBER')), current_user
from pg_roles r where r.rolname = current_user"""


def migrate_url(env: Mapping[str, str]) -> str:
    url = env.get("MIGRATE_DATABASE_URL") or env.get("DATABASE_URL") or ""
    if not url:
        raise ValueError("MIGRATE_DATABASE_URL or DATABASE_URL is required to migrate")
    return url


def retry_seconds(env: Mapping[str, str]) -> float:
    raw = env.get("MIGRATE_RETRY_TIMEOUT", "")
    if raw == "":
        return 300.0
    m = re.fullmatch(r"(\d+)(ms|s|m)?", raw)
    if m is None or (m.group(2) is None and m.group(1) != "0"):
        raise ValueError(f"MIGRATE_RETRY_TIMEOUT must be a duration such as 90s or 5m, got {raw!r}")
    return int(m.group(1)) * {"ms": 0.001, "s": 1.0, "m": 60.0}[m.group(2) or "s"]


def is_connection_error(err: BaseException) -> bool:
    """A database that could not be reached (retried), not a statement's own error, a rejected login or a missing database."""
    import psycopg

    state = getattr(err, "sqlstate", None) or ""
    if state.startswith("08") or state in ("57P01", "57P02", "57P03", "53300"):
        return True
    if state.startswith("28") or state == "3D000":
        return False
    text = str(err)
    if isinstance(err, psycopg.OperationalError) and not state:
        return not re.search(r"password authentication failed|does not exist|no pg_hba.conf entry", text)
    return False


def log(message: str, **fields: object) -> None:
    print(json.dumps({"level": "warn", "message": message, "service": "prediction", **fields}), file=sys.stderr)


def run(env: Mapping[str, str], sleep: Callable[[float], None] = time.sleep, warn: Callable[..., None] = log) -> None:
    import psycopg

    from prediction.store import migrate

    url = migrate_url(env)
    deadline = time.monotonic() + retry_seconds(env)
    wait = 1.0
    attempt = 0
    while True:
        attempt += 1
        try:
            with psycopg.connect(url, connect_timeout=10) as conn:
                row = conn.execute(APP_ROLE_QUERY).fetchone()
            break
        except psycopg.Error as err:
            if not is_connection_error(err) or time.monotonic() + wait > deadline:
                raise
            warn("migration database not reachable; retrying", attempt=attempt, retryInSeconds=wait, error=str(err))
            sleep(wait)
            wait = min(2 * wait, 30.0)
    if row and row[0] and env.get("MIGRATE_ALLOW_APP_ROLE") != "true":
        raise PermissionError(
            f"refusing to migrate as {row[1]!r}, the serving role {APP_ROLE} or a member of it: use the owner role's URL "
            "(MIGRATE_DATABASE_URL), or set MIGRATE_ALLOW_APP_ROLE=true where it owns the schema (dev only)")
    migrate(url)


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
