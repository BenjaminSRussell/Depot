"""Repository contract, run against the in-memory repository and, when PASSAGE_TEST_DATABASE_URL (owner) and
PASSAGE_TEST_APP_DATABASE_URL (passage_app) are set, against Postgres in a private schema."""

from __future__ import annotations

import os
import uuid
from datetime import date, datetime, timedelta, timezone

import pytest

from prediction import synth
from prediction.domain import Config, PaidInvoice, PredictionError
from prediction.service import SCORED_EVENT, InMemoryRepository, PredictionService, SyncState

OWNER_URL = os.environ.get("PASSAGE_TEST_DATABASE_URL", "")
APP_URL = os.environ.get("PASSAGE_TEST_APP_DATABASE_URL", "") or OWNER_URL
T1 = "11111111-1111-1111-1111-111111111111"
T2 = "22222222-2222-2222-2222-222222222222"
USER = "33333333-3333-3333-3333-333333333333"
NOW = datetime(2026, 11, 13, 10, 0, tzinfo=timezone.utc)


@pytest.fixture(scope="module")
def pg_schema():
    if not OWNER_URL:
        pytest.skip("PASSAGE_TEST_DATABASE_URL is not set")
    psycopg = pytest.importorskip("psycopg")
    from psycopg.conninfo import make_conninfo

    from prediction.store import migrate

    schema = f"prediction_{uuid.uuid4().hex[:12]}"
    with psycopg.connect(OWNER_URL, autocommit=True) as admin:
        admin.execute(f"create schema {schema}")
    try:
        migrate(make_conninfo(OWNER_URL, options=f"-c search_path={schema}"))
        yield make_conninfo(APP_URL, options=f"-c search_path={schema}"), make_conninfo(OWNER_URL, options=f"-c search_path={schema}")
    finally:
        with psycopg.connect(OWNER_URL, autocommit=True) as admin:
            admin.execute(f"drop schema {schema} cascade")


@pytest.fixture(params=["memory", "postgres"])
def make_repo(request):
    """A factory: calling it again returns a fresh handle on the same store (a restart for Postgres)."""
    if request.param == "memory":
        repo = InMemoryRepository()
        return lambda: repo
    app_url, _owner = request.getfixturevalue("pg_schema")
    from prediction.store import PgRepository

    return lambda: PgRepository(app_url)


def tenant() -> str:
    return str(uuid.uuid4())


def svc(repo, **cfg) -> PredictionService:
    # mae_max_days 30 (the top of the range) keeps models of these small synthetic sets ACTIVE.
    return PredictionService(repo, Config(**{"min_rows": 100, "mae_max_days": 30.0, **cfg}), clock=lambda: NOW)


def test_models_and_predictions_survive_a_restart(make_repo):
    t = tenant()
    model = svc(make_repo()).retrain(t, synth.paid_invoices(600))
    assert model.status == "ACTIVE"
    open_invoices = synth.open_invoices(5)
    first = svc(make_repo()).score(t, open_invoices)
    restarted = svc(make_repo())
    active = restarted.active_model(t)
    assert active is not None and active.model_version == model.model_version
    # The stored model scores exactly as the trained one did.
    assert restarted.score(t, open_invoices) == first
    found = restarted.prediction(t, first[0].invoice_id)
    assert found is not None and found[0] == first[0] and found[1] is False


def test_a_passing_retrain_retires_the_previous_active_model_and_a_failing_one_stays_candidate(make_repo):
    t = tenant()
    s = svc(make_repo())
    m1 = s.retrain(t, synth.paid_invoices(400))
    m2 = svc(make_repo(), mae_max_days=1.0).retrain(t, synth.paid_invoices(400))
    assert m2.status == "CANDIDATE"
    assert s.active_model(t).model_version == m1.model_version
    m3 = s.retrain(t, synth.paid_invoices(500))
    assert [(m.model_version, m.status) for m in make_repo().models(t)] == [
        (m1.model_version, "RETIRED"),
        (m2.model_version, "CANDIDATE"),
        (m3.model_version, "ACTIVE"),
    ]


def test_settings_are_per_tenant_and_drive_the_next_retrain(make_repo):
    t, other = tenant(), tenant()
    s = svc(make_repo(), min_rows=500)
    assert s.config_for(t) == Config(min_rows=500, mae_max_days=30.0)
    s.save_settings(t, USER, 200, None)
    assert svc(make_repo(), min_rows=500).config_for(t) == Config(min_rows=200, mae_max_days=30.0)
    assert s.config_for(other) == Config(min_rows=500, mae_max_days=30.0)
    assert s.retrain(t, synth.paid_invoices(250)).min_rows == 200
    with pytest.raises(PredictionError) as err:
        s.retrain(other, synth.paid_invoices(250))
    assert err.value.code == "AGT-0213"
    s.save_settings(t, USER, None, 12.5)
    assert s.config_for(t) == Config(min_rows=200, mae_max_days=12.5)


def test_paid_invoices_become_the_outcomes_of_the_accuracy_report(make_repo):
    t = tenant()
    s = svc(make_repo())
    s.retrain(t, synth.paid_invoices(400))
    invoices = synth.open_invoices(4)
    preds = s.score(t, invoices)
    assert s.accuracy(t)["count"] == 0
    # Two of them are paid: one on the predicted day, one 10 days later and more than 30 days past due.
    on_time, late = invoices[0], invoices[1]
    paid = [
        PaidInvoice(on_time.invoice_id, on_time.customer_id, on_time.amount, on_time.invoice_date, on_time.due_date, 0, 0, preds[0].predicted_payment_date),
        PaidInvoice(late.invoice_id, late.customer_id, late.amount, late.invoice_date, late.due_date, 0, 0, max(preds[1].predicted_payment_date + timedelta(days=10), late.due_date + timedelta(days=31))),
    ]
    assert make_repo().record_actuals(t, paid) == 2
    assert make_repo().record_actuals(t, paid) == 0  # once
    report = svc(make_repo()).accuracy(t)
    assert report["count"] == 2 and report["withinDays"] == 7
    errors = [0, abs((paid[1].paid_date - preds[1].predicted_payment_date).days)]
    assert report["maeDays"] == f"{sum(errors) / 2:.2f}"
    assert report["withinShare"] == "0.5000"
    assert sum(b["count"] for b in report["calibration"]) == 2
    assert [m["modelVersion"] for m in report["byModel"]] == [preds[0].model_version]
    assert svc(make_repo()).accuracy(tenant())["count"] == 0


def test_tenants_never_see_each_other(make_repo):
    s = svc(make_repo())
    t1, t2 = tenant(), tenant()
    s.retrain(t1, synth.paid_invoices(300))
    p = s.score(t1, synth.open_invoices(1))[0]
    assert s.active_model(t2) is None
    assert s.prediction(t2, p.invoice_id) is None
    with pytest.raises(PredictionError):
        s.score(t2, synth.open_invoices(1))


def test_scoring_writes_one_scored_event_per_invoice_in_the_same_transaction(pg_schema):
    import psycopg

    from prediction.store import PgRepository

    app_url, owner_url = pg_schema
    t = tenant()
    s = svc(PgRepository(app_url))
    s.retrain(t, synth.paid_invoices(300))
    preds = s.score(t, synth.open_invoices(3))
    with psycopg.connect(owner_url) as c:
        rows = c.execute("select type, aggregate_id, payload from outbox where tenant_id = %s order by aggregate_id", (t,)).fetchall()
    assert [r[0] for r in rows] == [SCORED_EVENT] * 3
    by_invoice = {r[1]: r[2] for r in rows}
    p = preds[0]
    assert by_invoice[p.invoice_id] == {
        "invoice_id": p.invoice_id,
        "model_version": p.model_version,
        "predicted_payment_date": p.predicted_payment_date.isoformat(),
        "p_late_30d": p.p_late_30d,
        "scored_at": p.scored_at.isoformat(),
    }


def test_the_application_role_is_bound_by_row_level_security(pg_schema):
    import psycopg

    from prediction.store import PgRepository

    app_url, _owner = pg_schema
    t = tenant()
    svc(PgRepository(app_url)).retrain(t, synth.paid_invoices(300))
    with psycopg.connect(app_url) as c:
        # No tenant set: nothing is visible and nothing can be written.
        assert c.execute("select count(*) from prediction_model").fetchone()[0] == 0
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            c.execute("insert into prediction_settings (tenant_id, min_rows, mae_max_days, updated_by) values (%s, 100, 6, 'x')", (t,))
    with psycopg.connect(app_url) as c:
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            c.execute("delete from payment_prediction")


def test_events_in_memory_mirror_the_outbox():
    repo = InMemoryRepository()
    s = svc(repo)
    s.retrain(T1, synth.paid_invoices(300))
    preds = s.score(T1, synth.open_invoices(2))
    assert [(t, e["invoice_id"]) for t, e in repo.events] == [(T1, p.invoice_id) for p in preds]
    assert date.fromisoformat(repo.events[0][1]["predicted_payment_date"]) == preds[0].predicted_payment_date


def test_sync_state_and_the_dso_forecast_are_per_tenant_and_legal_entity(make_repo):
    from prediction.service import SyncState

    t, other, le, le2 = tenant(), tenant(), str(uuid.uuid4()), str(uuid.uuid4())
    s = svc(make_repo())
    s.retrain(t, synth.paid_invoices(400))
    assert make_repo().sync_state(t, le) == SyncState()
    mark = datetime(2026, 11, 12, 8, 0, 5, tzinfo=timezone.utc)
    make_repo().save_sync_state(t, le, SyncState(mark, mark, NOW))
    assert make_repo().sync_state(t, le) == SyncState(mark, mark, NOW)
    assert make_repo().sync_state(t, le2) == SyncState() and make_repo().sync_state(other, le) == SyncState()
    assert make_repo().dso(t, le) is None
    invoices = synth.open_invoices(40)
    preds = s.score(t, invoices, le)
    make_repo().save_invoice_facts(t, le, invoices)
    dso = make_repo().dso(t, le)
    assert dso is not None and dso.open_invoices == 40
    total = sum(i.amount for i in invoices)
    expected = sum(i.amount * (p.predicted_payment_date - i.invoice_date).days for i, p in zip(invoices, preds)) / total
    assert abs(dso.dso_days - round(expected, 1)) < 0.06 and abs(float(dso.open_amount) - total) < 0.05 * len(invoices)
    assert 0 <= float(dso.avg_p_late_30d) <= 1
    assert make_repo().dso(t, le2) is None and make_repo().dso(other, le) is None
    # An invoice that was paid leaves the forecast.
    paid = PaidInvoice(**{**invoices[0].__dict__, "paid_date": preds[0].predicted_payment_date})
    make_repo().record_actuals(t, [paid])
    assert make_repo().dso(t, le).open_invoices == 39


def test_a_reversed_invoice_leaves_the_dso_forecast_but_keeps_its_prediction(make_repo):
    t, le = tenant(), str(uuid.uuid4())
    s = svc(make_repo())
    s.retrain(t, synth.paid_invoices(400))
    invoices = synth.open_invoices(10)
    s.score(t, invoices, le)
    make_repo().save_invoice_facts(t, le, invoices)
    assert make_repo().dso(t, le).open_invoices == 10
    assert make_repo().mark_reversed(t, [invoices[0].invoice_id, "never-scored"]) == 1
    assert make_repo().mark_reversed(t, [invoices[0].invoice_id]) == 0
    assert make_repo().dso(t, le).open_invoices == 9
    assert make_repo().prediction(t, invoices[0].invoice_id) is not None


def test_predictions_are_scoped_to_the_legal_entities_a_call_acts_for(make_repo):
    t, le, le2 = tenant(), str(uuid.uuid4()), str(uuid.uuid4())
    s = svc(make_repo())
    s.retrain(t, synth.paid_invoices(400))
    both = synth.open_invoices(6)
    mine, theirs = both[:3], both[3:]
    scored = s.score(t, mine, le, [le])
    s.score(t, theirs, le2, [le2])
    first = scored[0].invoice_id
    repo = make_repo()
    # Every entity of the tenant ('*'), the entity itself and a list holding it see the prediction; another entity and none do not.
    assert repo.prediction(t, first) is not None and repo.prediction(t, first, [le]) is not None and repo.prediction(t, first, [le2, le]) is not None
    assert repo.prediction(t, first, [le2]) is None and repo.prediction(t, first, []) is None
    # A call scoped to entities stores for one of them, and cannot take over an invoice another entity holds.
    with pytest.raises(PredictionError) as err:
        repo.save_predictions(t, scored, None, [le])
    assert err.value.code == "PRD-0403" and err.value.status == 403
    with pytest.raises(PredictionError) as err:
        repo.save_predictions(t, scored, le2, [le2])
    assert err.value.code == "PRD-0403"
    # Outcomes and actuals follow the scope too.
    paid = PaidInvoice(**{**mine[0].__dict__, "paid_date": scored[0].predicted_payment_date})
    assert repo.record_actuals(t, [paid], [le2]) == 0 and repo.outcomes(t, [le2]) == []
    assert repo.record_actuals(t, [paid], [le]) == 1 and len(repo.outcomes(t, [le])) == 1 and repo.outcomes(t, [le2]) == []
    assert repo.mark_reversed(t, [theirs[0].invoice_id], [le]) == 0 and repo.mark_reversed(t, [theirs[0].invoice_id], [le2]) == 1


@pytest.mark.skipif(not OWNER_URL, reason="PASSAGE_TEST_DATABASE_URL is not set")
def test_the_database_hides_other_legal_entities_and_sessions_without_a_scope(pg_schema):
    import psycopg

    from prediction.store import PgRepository

    app_url, _owner = pg_schema
    t, le, le2 = tenant(), str(uuid.uuid4()), str(uuid.uuid4())
    s = svc(PgRepository(app_url))
    s.retrain(t, synth.paid_invoices(400))
    s.score(t, synth.open_invoices(4), le, [le])
    repo = PgRepository(app_url)
    repo.save_sync_state(t, le, SyncState(NOW, NOW, NOW, NOW))

    def count(sql: str, scopes: str | None) -> int:
        with psycopg.connect(app_url) as conn:
            with conn.transaction():
                conn.execute("select set_config('passage.tenant_id', %s, true)", (t,))
                if scopes is not None:
                    conn.execute("select set_config('passage.legal_entity_scopes', %s, true)", (scopes,))
                return int(conn.execute(sql).fetchone()[0])  # type: ignore[index]

    for table in ("payment_prediction", "prediction_sync"):
        sql = f"select count(*) from {table}"
        assert count(sql, "*") > 0 and count(sql, le) == count(sql, "*") and count(sql, f"{le2},{le}") == count(sql, "*")
        assert count(sql, le2) == 0 and count(sql, "") == 0
        # A session that sets no scope at all sees nothing, whatever the tenant says.
        assert count(sql, None) == 0
    with pytest.raises(psycopg.errors.InsufficientPrivilege):
        with psycopg.connect(app_url) as conn:
            with conn.transaction():
                conn.execute("select set_config('passage.tenant_id', %s, true), set_config('passage.legal_entity_scopes', %s, true)", (t, le2))
                conn.execute("insert into prediction_sync (tenant_id, legal_entity_id) values (%s, %s)", (t, le))
    assert repo.sync_state(t, le2) == SyncState() and repo.sync_state(t, le).open_watermark == NOW


def test_predictions_with_no_entity_are_claimed_by_the_entity_the_feed_says_they_belong_to(make_repo):
    t, le, le2 = tenant(), str(uuid.uuid4()), str(uuid.uuid4())
    s = svc(make_repo())
    s.retrain(t, synth.paid_invoices(400))
    invoices = synth.open_invoices(4)
    first = s.score(t, invoices)  # stored for no entity (as before the entity was kept)
    repo = make_repo()
    ids = [p.invoice_id for p in first]
    # An entity-scoped session cannot see them and cannot overwrite them: that is what stopped the sync.
    assert repo.prediction(t, ids[0], [le]) is None
    with pytest.raises(PredictionError):
        repo.save_predictions(t, first, le, [le])
    # The sync claims the unowned ones for its entity first; then it scores them as usual.
    assert repo.claim_unowned(t, le, ids) == 4 and repo.claim_unowned(t, le2, ids) == 0
    assert repo.prediction(t, ids[0], [le]) is not None and repo.prediction(t, ids[0], [le2]) is None
    s.score(t, invoices, le, [le])
    assert repo.prediction(t, ids[0], [le]) is not None


def test_the_consumer_inbox_dedupes_per_tenant_and_a_failed_block_leaves_no_record(make_repo):
    t, other = tenant(), tenant()
    repo = make_repo()
    with repo.inbox_once(t, "prediction-receivables", "evt-1") as first:
        assert first
    with make_repo().inbox_once(t, "prediction-receivables", "evt-1") as again:  # a restart sees the record
        assert not again
    with repo.inbox_once(other, "prediction-receivables", "evt-1") as elsewhere, repo.inbox_once(t, "another-consumer", "evt-1") as other_consumer:
        assert elsewhere and other_consumer
    with pytest.raises(RuntimeError):
        with repo.inbox_once(t, "prediction-receivables", "evt-2") as first:
            assert first
            raise RuntimeError("the recompute failed")
    with repo.inbox_once(t, "prediction-receivables", "evt-2") as retried:
        assert retried  # nothing was recorded, so the redelivered event is handled


def test_the_application_role_may_only_read_and_insert_inbox_rows(pg_schema):
    import psycopg

    app_url, _owner = pg_schema
    t = tenant()
    with psycopg.connect(app_url) as c, c.transaction():
        c.execute("select set_config('passage.tenant_id', %s, true)", (t,))
        c.execute("insert into consumer_inbox (tenant_id, consumer, event_id) values (%s, 'c', 'e')", (t,))
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            c.execute("delete from consumer_inbox")


def test_the_consumer_inbox_records_an_outcome_and_keeps_it_across_a_restart(make_repo):
    t = tenant()
    repo = make_repo()
    with repo.inbox_once(t, "c", "e1") as first:
        assert first
    assert repo.inbox_entry(t, "c", "e1") == ("done", None)
    repo.inbox_outcome(t, "c", "e1", "failed", "receivables answered 403")
    assert make_repo().inbox_entry(t, "c", "e1") == ("failed", "receivables answered 403")
    assert make_repo().inbox_entry(tenant(), "c", "e1") is None
