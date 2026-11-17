"""`python -m prediction.migrate`, the chart's owner-role migration Job (D28): the owner URL wins, a missing URL fails the
Job, and against Postgres (DEPOT_TEST_DATABASE_URL) the files are applied once in a private schema."""

from __future__ import annotations

import os
import uuid

import pytest

from prediction.migrate import main, migrate_url


def test_owner_url_wins_over_the_app_url():
    assert migrate_url({"MIGRATE_DATABASE_URL": "postgres://owner", "DATABASE_URL": "postgres://app"}) == "postgres://owner"
    assert migrate_url({"DATABASE_URL": "postgres://app"}) == "postgres://app"
    with pytest.raises(ValueError):
        migrate_url({})


def test_missing_url_fails_the_job(capsys):
    assert main({}) == 1
    assert "migrations failed" in capsys.readouterr().err


def test_applies_the_migrations_once():
    owner = os.environ.get("DEPOT_TEST_DATABASE_URL", "")
    if not owner:
        pytest.skip("DEPOT_TEST_DATABASE_URL is not set")
    psycopg = pytest.importorskip("psycopg")
    from psycopg.conninfo import make_conninfo

    from prediction.store import MIGRATIONS_DIR

    schema = f"prediction_migrate_{uuid.uuid4().hex[:12]}"
    with psycopg.connect(owner, autocommit=True) as admin:
        admin.execute(f"create schema {schema}")
    try:
        env = {"MIGRATE_DATABASE_URL": make_conninfo(owner, options=f"-c search_path={schema}"), "DATABASE_URL": "postgresql://depot_app@unreachable.invalid/x"}
        assert main(env) == 0
        assert main(env) == 0  # a Job retry or the next sync applies nothing
        with psycopg.connect(owner) as conn:
            applied = conn.execute(f"select count(*) from {schema}.schema_migrations").fetchone()[0]
        assert applied == len(list(MIGRATIONS_DIR.glob("*.sql")))
    finally:
        with psycopg.connect(owner, autocommit=True) as admin:
            admin.execute(f"drop schema {schema} cascade")
