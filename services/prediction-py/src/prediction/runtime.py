"""Process wiring shared by the HTTP service and the sync jobs: the repository (Postgres when DATABASE_URL is set) and the defaults."""

from __future__ import annotations

import os

from prediction.domain import Config
from prediction.service import InMemoryRepository, Repository


def config_from_env() -> Config:
    return Config(
        min_rows=int(os.environ.get("PREDICTION_MIN_ROWS", "500")),
        mae_max_days=float(os.environ.get("PREDICTION_MAE_MAX_DAYS", "6")),
    )


def repository_from_env() -> Repository:
    """Postgres when DATABASE_URL is set (the chart always sets it); in memory otherwise (tests, local runs)."""
    url = os.environ.get("DATABASE_URL", "")
    if not url:
        return InMemoryRepository()
    from prediction.store import PgRepository, migrate

    if os.environ.get("MIGRATE_ON_START") == "true":
        migrate(url)
    return PgRepository(url)
