"""`python -m prediction.migrate`, the chart's owner-role migration Job (D28), with dbx.RunMigrateCommand's contract: the
owner URL wins, a missing URL fails the Job, an unreachable database is retried until MIGRATE_RETRY_TIMEOUT, and against
Postgres (DEPOT_TEST_DATABASE_URL) the files are applied once in a private schema, under the advisory lock, while depot_app
(DEPOT_TEST_APP_DATABASE_URL) is refused unless MIGRATE_ALLOW_APP_ROLE=true."""

from __future__ import annotations

import os
import threading
import uuid

import pytest

from prediction.migrate import main, migrate_url, retry_seconds, run


def test_owner_url_wins_over_the_app_url():
    assert migrate_url({"MIGRATE_DATABASE_URL": "postgres://owner", "DATABASE_URL": "postgres://app"}) == "postgres://owner"
    assert migrate_url({"DATABASE_URL": "postgres://app"}) == "postgres://app"
    with pytest.raises(ValueError):
        migrate_url({})


def test_missing_url_fails_the_job(capsys):
    assert main({}) == 1
    assert "migrations failed" in capsys.readouterr().err


def test_the_retry_bound_is_configurable():
    assert retry_seconds({}) == 300.0
    assert retry_seconds({"MIGRATE_RETRY_TIMEOUT": "90s"}) == 90.0
    assert retry_seconds({"MIGRATE_RETRY_TIMEOUT": "2m"}) == 120.0
    assert retry_seconds({"MIGRATE_RETRY_TIMEOUT": "0"}) == 0.0
    with pytest.raises(ValueError):
        retry_seconds({"MIGRATE_RETRY_TIMEOUT": "soon"})


def test_an_unreachable_database_is_retried_until_the_bound():
    psycopg = pytest.importorskip("psycopg")
    waits, warnings = [], []
    with pytest.raises(psycopg.OperationalError):
        run({"MIGRATE_DATABASE_URL": "postgresql://owner@127.0.0.1:1/x", "MIGRATE_RETRY_TIMEOUT": "20s"},
            sleep=waits.append, warn=lambda *a, **k: warnings.append(k))
    assert waits[:3] == [1.0, 2.0, 4.0] and len(warnings) == len(waits) >= 3


def _schema_url(url: str, schema: str) -> str:
    from psycopg.conninfo import make_conninfo

    return make_conninfo(url, options=f"-c search_path={schema}")


def test_applies_the_migrations_once():
    owner = os.environ.get("DEPOT_TEST_DATABASE_URL", "")
    if not owner:
        pytest.skip("DEPOT_TEST_DATABASE_URL is not set")
    psycopg = pytest.importorskip("psycopg")

    from prediction.store import MIGRATIONS_DIR

    schema = f"prediction_migrate_{uuid.uuid4().hex[:12]}"
    with psycopg.connect(owner, autocommit=True) as admin:
        admin.execute(f"create schema {schema}")
    try:
        env = {"MIGRATE_DATABASE_URL": _schema_url(owner, schema), "DATABASE_URL": "postgresql://depot_app@unreachable.invalid/x"}
        results = []
        runs = [threading.Thread(target=lambda: results.append(main(env))) for _ in range(2)]  # overlapping runs take turns
        for r in runs:
            r.start()
        for r in runs:
            r.join()
        assert results == [0, 0]
        assert main(env) == 0  # a Job retry or the next sync applies nothing
        with psycopg.connect(owner) as conn:
            applied = conn.execute(f"select count(*) from {schema}.schema_migrations").fetchone()[0]
        assert applied == len(list(MIGRATIONS_DIR.glob("*.sql")))
    finally:
        with psycopg.connect(owner, autocommit=True) as admin:
            admin.execute(f"drop schema {schema} cascade")


def test_refuses_the_app_role():
    owner, app = os.environ.get("DEPOT_TEST_DATABASE_URL", ""), os.environ.get("DEPOT_TEST_APP_DATABASE_URL", "")
    if not owner or not app:
        pytest.skip("DEPOT_TEST_DATABASE_URL and DEPOT_TEST_APP_DATABASE_URL are not set")
    psycopg = pytest.importorskip("psycopg")
    schema = f"prediction_app_{uuid.uuid4().hex[:12]}"
    with psycopg.connect(owner, autocommit=True) as admin:
        admin.execute(f"create schema {schema}")
        admin.execute(f"grant usage, create on schema {schema} to depot_app")
    try:
        env = {"MIGRATE_DATABASE_URL": _schema_url(app, schema), "MIGRATE_RETRY_TIMEOUT": "0"}
        with pytest.raises(PermissionError, match="refusing to migrate"):
            run(env)
        with psycopg.connect(owner) as conn:
            assert conn.execute("select count(*) from pg_tables where schemaname = %s", (schema,)).fetchone()[0] == 0
    finally:
        with psycopg.connect(owner, autocommit=True) as admin:
            admin.execute(f"drop schema {schema} cascade")
