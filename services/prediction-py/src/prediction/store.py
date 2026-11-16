"""Postgres repository (migrations/0001_prediction.sql). Every statement runs in a transaction that sets
passage.tenant_id and passage.legal_entity_scopes, so row-level security answers only the caller's tenant and, on the tables that carry
a legal entity (payment_prediction, prediction_sync; migrations/0003_legal_entity_scope.sql), only the legal entities the call acts for:
a sync job the entity it runs for, an API read the caller's X-Passage-Legal-Entity ('*' for a caller who holds the all-scope duty)."""

from __future__ import annotations

import os
import uuid
from contextlib import contextmanager
from decimal import Decimal
from pathlib import Path
from typing import Iterator

import psycopg
from psycopg.types.json import Jsonb

from prediction.domain import Config, Model, OpenInvoice, Outcome, PaidInvoice, Prediction, PredictionError, model_artifact, model_from_artifact
from prediction.service import SCORED_EVENT, Dso, Entities, SyncState, scored_event

# The image copies the migrations to /app/migrations (Dockerfile); a checkout reads them from the service directory.
MIGRATIONS_DIR = Path(os.environ.get("PREDICTION_MIGRATIONS_DIR") or Path(__file__).resolve().parents[2] / "migrations")


def migrate(url: str, directory: Path = MIGRATIONS_DIR) -> None:
    """Applies the migrations not yet in schema_migrations, each in its own transaction (dbx.Migrate contract).
    Run as the owner role."""
    with psycopg.connect(url, autocommit=True) as conn:
        conn.execute("create table if not exists schema_migrations (name text primary key, applied_at timestamptz not null default now())")
        for path in sorted(directory.glob("*.sql")):
            if conn.execute("select 1 from schema_migrations where name = %s", (path.name,)).fetchone():
                continue
            with conn.transaction():
                conn.execute(path.read_text(encoding="utf-8"))  # type: ignore[arg-type]
                conn.execute("insert into schema_migrations (name) values (%s)", (path.name,))


class PgRepository:
    def __init__(self, url: str) -> None:
        self.url = url

    @contextmanager
    def _tenant(self, tenant_id: str, entities: Entities = None) -> Iterator[psycopg.Connection]:
        """A transaction for the tenant. `entities` are the legal entities the call acts for: None is every one ('*'), a sequence is those
        (comma-joined, as the Go services' dbx sets it) and an empty one sees nothing."""
        str(uuid.UUID(tenant_id))  # never set a malformed tenant
        scopes = "*" if entities is None else ",".join(str(uuid.UUID(e)) for e in entities)  # never set a malformed entity
        with psycopg.connect(self.url) as conn:
            with conn.transaction():
                conn.execute("select set_config('passage.tenant_id', %s, true), set_config('passage.legal_entity_scopes', %s, true)", (tenant_id, scopes))
                yield conn

    @contextmanager
    def inbox_once(self, tenant_id: str, consumer: str, event_id: str) -> Iterator[bool]:
        """Records the event in consumer_inbox and yields True when it was new, False when it was seen before. The row is written in a
        transaction that surrounds the block: a block that raises rolls the row back, so the event is handled again on redelivery, and a
        concurrent delivery of the same event waits for this one's outcome. The block's own writes (the recompute) go through their own
        transactions and are idempotent upserts, so unlike Go's PgInbox.Once the row and the effect are not one transaction: a crash after
        the effect and before this commit repeats the effect (at-least-once)."""
        with self._tenant(tenant_id) as c:
            cur = c.execute("insert into consumer_inbox (tenant_id, consumer, event_id) values (%s, %s, %s) on conflict do nothing", (tenant_id, consumer, event_id))
            yield cur.rowcount == 1

    def inbox_outcome(self, tenant_id: str, consumer: str, event_id: str, outcome: str, detail: str) -> None:
        with self._tenant(tenant_id) as c:
            c.execute("update consumer_inbox set outcome = %s, detail = %s where tenant_id = %s and consumer = %s and event_id = %s", (outcome, detail[:500], tenant_id, consumer, event_id))

    def inbox_entry(self, tenant_id: str, consumer: str, event_id: str) -> tuple[str, str | None] | None:
        with self._tenant(tenant_id) as c:
            row = c.execute("select outcome, detail from consumer_inbox where tenant_id = %s and consumer = %s and event_id = %s", (tenant_id, consumer, event_id)).fetchone()
        return None if row is None else (row[0], row[1])

    def ping(self) -> None:
        with psycopg.connect(self.url, connect_timeout=2) as conn:
            conn.execute("select 1")

    def models(self, tenant_id: str) -> list[Model]:
        with self._tenant(tenant_id) as c:
            rows = c.execute(
                "select model_version, trained_at, rows, mae_days, status, min_rows, mae_max_days, artifact from prediction_model order by trained_at, model_version"
            ).fetchall()
        return [self._model(r) for r in rows]

    def model_count(self, tenant_id: str) -> int:
        with self._tenant(tenant_id) as c:
            return int(c.execute("select count(*) from prediction_model").fetchone()[0])  # type: ignore[index]

    def active_model(self, tenant_id: str) -> Model | None:
        with self._tenant(tenant_id) as c:
            row = c.execute(
                "select model_version, trained_at, rows, mae_days, status, min_rows, mae_max_days, artifact from prediction_model where status = 'ACTIVE'"
            ).fetchone()
        return None if row is None else self._model(row)

    @staticmethod
    def _model(r: tuple) -> Model:  # type: ignore[type-arg]
        version, trained_at, rows, mae, status, min_rows, mae_max, artifact = r
        return model_from_artifact(
            artifact, model_version=version, trained_at=trained_at, rows=rows, mae_days=float(mae), status=status, min_rows=min_rows, mae_max_days=float(mae_max)
        )

    def save_model(self, tenant_id: str, model: Model) -> None:
        try:
            with self._tenant(tenant_id) as c:
                # One retrain at a time per tenant, so the ACTIVE swap and the version number never race.
                c.execute("select pg_advisory_xact_lock(hashtextextended('prediction_model:' || %s, 0))", (tenant_id,))
                if model.status == "ACTIVE":
                    c.execute("update prediction_model set status = 'RETIRED' where status = 'ACTIVE'")
                c.execute(
                    """insert into prediction_model (tenant_id, model_version, trained_at, rows, mae_days, status, min_rows, mae_max_days, artifact)
                       values (%s, %s, %s, %s, %s, %s, %s, %s, %s)""",
                    (tenant_id, model.model_version, model.trained_at, model.rows, Decimal(str(model.mae_days)), model.status, model.min_rows, Decimal(str(model.mae_max_days)), Jsonb(model_artifact(model))),
                )
        except psycopg.errors.UniqueViolation as exc:
            raise PredictionError("AGT-0409", f"model {model.model_version} was stored by a concurrent retrain; retrain again", 409) from exc

    def save_predictions(self, tenant_id: str, predictions: list[Prediction], legal_entity_id: str | None = None, entities: Entities = None) -> None:
        """Upserts the predictions for `legal_entity_id` and writes one prediction.payment.scored outbox row each, in one transaction. A call
        scoped to legal entities stores for one of them; an invoice that already belongs to an entity outside the scope is refused (PRD-0403)."""
        if entities is not None and legal_entity_id is None:
            raise PredictionError("PRD-0403", "a call scoped to legal entities stores predictions for one of them", 403)
        try:
            with self._tenant(tenant_id, entities) as c:
                with c.cursor() as cur:
                    cur.executemany(
                        """insert into payment_prediction (tenant_id, invoice_id, model_version, predicted_payment_date, p_late_30d, features_hash, due_date, scored_at, legal_entity_id)
                           values (%s, %s, %s, %s, %s, %s, %s, %s, %s)
                           on conflict (tenant_id, invoice_id) do update set model_version = excluded.model_version,
                             predicted_payment_date = excluded.predicted_payment_date, p_late_30d = excluded.p_late_30d,
                             features_hash = excluded.features_hash, due_date = excluded.due_date, scored_at = excluded.scored_at,
                             actual_paid_date = null, legal_entity_id = coalesce(excluded.legal_entity_id, payment_prediction.legal_entity_id)""",
                        [(tenant_id, p.invoice_id, p.model_version, p.predicted_payment_date, Decimal(p.p_late_30d), p.features_hash, p.due_date, p.scored_at, legal_entity_id) for p in predictions],
                    )
                    cur.executemany(
                        "insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values (%s, %s, 'payment_prediction', %s, %s, %s)",
                        [(str(uuid.uuid4()), tenant_id, p.invoice_id, SCORED_EVENT, Jsonb(scored_event(p))) for p in predictions],
                    )
        except psycopg.errors.InsufficientPrivilege as exc:
            raise PredictionError("PRD-0403", "an invoice of this call belongs to a legal entity outside its scope", 403) from exc

    def prediction(self, tenant_id: str, invoice_id: str, entities: Entities = None) -> Prediction | None:
        with self._tenant(tenant_id, entities) as c:
            r = c.execute(
                "select invoice_id, model_version, predicted_payment_date, p_late_30d, features_hash, scored_at, due_date from payment_prediction where invoice_id = %s",
                (invoice_id,),
            ).fetchone()
        if r is None:
            return None
        return Prediction(r[0], r[1], r[2], f"{r[3]:.4f}", r[4], r[5], r[6])

    def settings(self, tenant_id: str) -> Config | None:
        with self._tenant(tenant_id) as c:
            r = c.execute("select min_rows, mae_max_days from prediction_settings").fetchone()
        return None if r is None else Config(min_rows=r[0], mae_max_days=float(r[1]))

    def save_settings(self, tenant_id: str, config: Config, user_id: str) -> None:
        with self._tenant(tenant_id) as c:
            c.execute(
                """insert into prediction_settings (tenant_id, min_rows, mae_max_days, updated_by) values (%s, %s, %s, %s)
                   on conflict (tenant_id) do update set min_rows = excluded.min_rows, mae_max_days = excluded.mae_max_days,
                     updated_by = excluded.updated_by, updated_at = now()""",
                (tenant_id, config.min_rows, Decimal(str(config.mae_max_days)), user_id),
            )

    def record_actuals(self, tenant_id: str, paid: list[PaidInvoice], entities: Entities = None) -> int:
        """Stamps the paid date on each stored prediction of a paid invoice (of the legal entities in scope), once."""
        if not paid:
            return 0
        with self._tenant(tenant_id, entities) as c:
            res = c.execute(
                """update payment_prediction p set actual_paid_date = v.paid_date
                     from (select * from unnest(%s::text[], %s::date[]) as t(invoice_id, paid_date)) v
                    where p.invoice_id = v.invoice_id and p.actual_paid_date is null""",
                ([i.invoice_id for i in paid], [i.paid_date for i in paid]),
            )
            return res.rowcount

    def outcomes(self, tenant_id: str, entities: Entities = None) -> list[Outcome]:
        with self._tenant(tenant_id, entities) as c:
            rows = c.execute(
                "select model_version, predicted_payment_date, p_late_30d, due_date, actual_paid_date from payment_prediction where actual_paid_date is not null order by invoice_id"
            ).fetchall()
        return [Outcome(r[0], r[1], f"{r[2]:.4f}", r[3], r[4]) for r in rows]


    def sync_state(self, tenant_id: str, legal_entity_id: str) -> SyncState:
        with self._tenant(tenant_id, [legal_entity_id]) as c:
            r = c.execute(
                "select open_watermark, paid_watermark, reversed_watermark, last_retrain_at from prediction_sync where legal_entity_id = %s", (legal_entity_id,)
            ).fetchone()
        return SyncState(*r) if r else SyncState()

    def save_sync_state(self, tenant_id: str, legal_entity_id: str, state: SyncState) -> None:
        with self._tenant(tenant_id, [legal_entity_id]) as c:
            c.execute(
                """insert into prediction_sync (tenant_id, legal_entity_id, open_watermark, paid_watermark, reversed_watermark, last_retrain_at, last_scored_at)
                   values (%s, %s, %s, %s, %s, %s, now())
                   on conflict (tenant_id, legal_entity_id) do update set open_watermark = excluded.open_watermark,
                     paid_watermark = excluded.paid_watermark, reversed_watermark = excluded.reversed_watermark, last_retrain_at = excluded.last_retrain_at, last_scored_at = excluded.last_scored_at""",
                (tenant_id, legal_entity_id, state.open_watermark, state.paid_watermark, state.reversed_watermark, state.last_retrain_at),
            )

    def save_invoice_facts(self, tenant_id: str, legal_entity_id: str, invoices: list[OpenInvoice]) -> None:
        """Stores the legal entity, invoice date and amount on the invoices' predictions, which the DSO forecast reads."""
        if not invoices:
            return
        with self._tenant(tenant_id, [legal_entity_id]) as c:
            c.execute(
                """update payment_prediction p set legal_entity_id = %s, invoice_date = v.invoice_date, amount = v.amount
                     from (select * from unnest(%s::text[], %s::date[], %s::numeric[]) as t(invoice_id, invoice_date, amount)) v
                    where p.invoice_id = v.invoice_id""",
                (legal_entity_id, [i.invoice_id for i in invoices], [i.invoice_date for i in invoices], [Decimal(str(i.amount)) for i in invoices]),
            )

    def dso(self, tenant_id: str, legal_entity_id: str) -> Dso | None:
        with self._tenant(tenant_id, [legal_entity_id]) as c:
            r = c.execute(
                """select sum(amount * (predicted_payment_date - invoice_date)) / nullif(sum(amount), 0), count(*), sum(amount),
                          sum(amount * p_late_30d) / nullif(sum(amount), 0), max(scored_at)
                     from payment_prediction
                    where legal_entity_id = %s and actual_paid_date is null and reversed_at is null and invoice_date is not null and amount is not null""",
                (legal_entity_id,),
            ).fetchone()
        if r is None or r[0] is None or not r[1]:
            return None
        return Dso(round(float(r[0]), 1), int(r[1]), f"{r[2]:.2f}", f"{r[3]:.4f}", r[4])

    def claim_unowned(self, tenant_id: str, legal_entity_id: str, invoice_ids: list[str]) -> int:
        """Gives the predictions of these invoices that belong to no legal entity (stored before the entity was kept, or by an all-scope caller)
        the entity the receivables feed says they are of. Only the predictions with no entity are touched, and it runs in a '*' session because
        a session scoped to an entity cannot see them; the invoice ids come from that entity's own receivables read."""
        if not invoice_ids:
            return 0
        str(uuid.UUID(legal_entity_id))
        with self._tenant(tenant_id, None) as c:
            return c.execute(
                "update payment_prediction set legal_entity_id = %s where invoice_id = any(%s) and legal_entity_id is null", (legal_entity_id, invoice_ids)
            ).rowcount

    def mark_reversed(self, tenant_id: str, invoice_ids: list[str], entities: Entities = None) -> int:
        """Takes reversed invoices out of the DSO forecast; their predictions stay for the record."""
        if not invoice_ids:
            return 0
        with self._tenant(tenant_id, entities) as c:
            return c.execute(
                "update payment_prediction set reversed_at = current_date where invoice_id = any(%s) and reversed_at is null", (invoice_ids,)
            ).rowcount
