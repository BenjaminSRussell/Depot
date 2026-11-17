"""`python -m prediction.migrate`: applies the migrations as the owner role and exits, the chart's PreSync migration Job (D28).

The owner role's URL is MIGRATE_DATABASE_URL, else DATABASE_URL (the same contract as dbx.RunMigrateCommand in the Go
services); serving pods keep MIGRATE_ON_START=false and connect as depot_app."""

from __future__ import annotations

import json
import os
import sys
from typing import Mapping


def migrate_url(env: Mapping[str, str]) -> str:
    url = env.get("MIGRATE_DATABASE_URL") or env.get("DATABASE_URL") or ""
    if not url:
        raise ValueError("MIGRATE_DATABASE_URL or DATABASE_URL is required to migrate")
    return url


def main(env: Mapping[str, str] = os.environ) -> int:
    try:
        from prediction.store import migrate

        migrate(migrate_url(env))
    except Exception as err:  # noqa: BLE001 - any failure fails the Job
        print(json.dumps({"level": "error", "message": "migrations failed", "service": "prediction", "error": str(err)}), file=sys.stderr)
        return 1
    print(json.dumps({"level": "info", "message": "migrations applied", "service": "prediction"}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
