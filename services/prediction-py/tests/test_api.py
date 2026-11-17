"""API tests. They need the dev extras (fastapi, httpx): pip install -e '.[dev]'."""

import pytest

pytest.importorskip("fastapi")
pytest.importorskip("httpx")

from fastapi.testclient import TestClient  # noqa: E402

from prediction import api, synth  # noqa: E402
from prediction.domain import Config  # noqa: E402
from prediction.service import InMemoryRepository, PredictionService  # noqa: E402

TENANT_A = "11111111-1111-1111-1111-111111111111"
TENANT_B = "22222222-2222-2222-2222-222222222222"
USER = "33333333-3333-3333-3333-333333333333"


@pytest.fixture()
def client():
    api.service = PredictionService(InMemoryRepository(), Config())
    return TestClient(api.app)


def h(tenant=TENANT_A, duties="agents.admin,collections.view,org.switcher.all-scope", entity=None):
    out = {"x-depot-tenant": tenant, "x-depot-user": USER, "x-depot-duties": duties}
    if entity is not None:
        out["x-depot-legal-entity"] = entity
    return out


def paid_body(n):
    return {
        "paidInvoices": [
            {"invoiceId": p.invoice_id, "customerId": p.customer_id, "amount": p.amount, "invoiceDate": p.invoice_date.isoformat(), "dueDate": p.due_date.isoformat(),
             "dunningCount": p.dunning_count, "disputes": p.disputes, "paidDate": p.paid_date.isoformat()}
            for p in synth.paid_invoices(n)
        ]
    }


def open_body(n):
    return {
        "legalEntityId": LE,
        "openInvoices": [
            {"invoiceId": i.invoice_id, "customerId": i.customer_id, "amount": i.amount, "invoiceDate": i.invoice_date.isoformat(), "dueDate": i.due_date.isoformat(),
             "dunningCount": i.dunning_count, "disputes": i.disputes}
            for i in synth.open_invoices(n)
        ]
    }


def test_health(client):
    assert client.get("/healthz").status_code == 200 and client.get("/readyz").status_code == 200


def test_retrain_score_and_read_prediction(client):
    r = client.post("/api/v1/predictions/payment/retrain", json=paid_body(600), headers=h())
    assert r.status_code == 201, r.text
    assert r.json()["status"] == "ACTIVE" and r.json()["rows"] == 600
    s = client.post("/api/v1/predictions/payment/score", json=open_body(3), headers=h())
    assert s.status_code == 200
    first = s.json()["items"][0]
    g = client.get(f"/api/v1/ar/invoices/{first['invoiceId']}/prediction", headers=h(duties="collections.view,org.switcher.all-scope"))
    assert g.status_code == 200
    assert g.json()["predictedPaymentDate"] == first["predictedPaymentDate"] and g.json()["stale"] is False


def test_too_few_rows_is_agt_0213(client):
    r = client.post("/api/v1/predictions/payment/retrain", json=paid_body(100), headers=h())
    assert r.status_code == 422 and r.json()["code"] == "AGT-0213"
    s = client.post("/api/v1/predictions/payment/score", json=open_body(1), headers=h())
    assert s.status_code == 422 and s.json()["code"] == "AGT-0213"


def test_identity_and_duties(client):
    assert client.post("/api/v1/predictions/payment/retrain", json=paid_body(1)).status_code == 401
    nil = h(tenant="00000000-0000-0000-0000-000000000000")
    assert client.get("/api/v1/ar/invoices/x/prediction", headers=nil).status_code == 401
    assert client.get("/api/v1/ar/invoices/x/prediction", headers=h(tenant="not-a-uuid")).status_code == 401
    denied = client.post("/api/v1/predictions/payment/retrain", json=paid_body(1), headers=h(duties="collections.view,org.switcher.all-scope"))
    assert denied.status_code == 403 and denied.json()["code"] == "AGT-0203"
    assert client.get("/api/v1/ar/invoices/x/prediction", headers=h(duties="agents.admin")).status_code == 403


def test_tenant_isolation_over_http(client):
    client.post("/api/v1/predictions/payment/retrain", json=paid_body(600), headers=h())
    scored = client.post("/api/v1/predictions/payment/score", json=open_body(1), headers=h()).json()["items"][0]
    path = f"/api/v1/ar/invoices/{scored['invoiceId']}/prediction"
    assert client.get(path, headers=h()).status_code == 200
    assert client.get(path, headers=h(tenant=TENANT_B)).status_code == 404
    assert client.post("/api/v1/predictions/payment/score", json=open_body(1), headers=h(tenant=TENANT_B)).status_code == 422


def test_validation_is_a_400_problem(client):
    bad = paid_body(1)
    bad["paidInvoices"][0]["tenantId"] = TENANT_B  # the tenant never comes from the body
    r = client.post("/api/v1/predictions/payment/retrain", json=bad, headers=h())
    assert r.status_code == 400 and r.json()["code"] == "PRD-0400"
    assert r.headers["content-type"].startswith("application/problem+json")


def test_settings_are_per_tenant_and_need_the_admin_duty(client):
    assert client.get("/api/v1/predictions/payment/settings", headers=h()).json() == {"minRows": 500, "maeMaxDays": 6.0, "tenantOverride": False}
    r = client.put("/api/v1/predictions/payment/settings", json={"minRows": 200}, headers=h())
    assert r.status_code == 200 and r.json() == {"minRows": 200, "maeMaxDays": 6.0, "tenantOverride": True}
    assert client.get("/api/v1/predictions/payment/settings", headers=h(TENANT_B)).json()["minRows"] == 500
    assert client.put("/api/v1/predictions/payment/settings", json={"minRows": 99}, headers=h()).status_code == 400
    assert client.put("/api/v1/predictions/payment/settings", json={"maeMaxDays": 31}, headers=h()).status_code == 400
    assert client.put("/api/v1/predictions/payment/settings", json={"tenantId": TENANT_B}, headers=h()).status_code == 400
    assert client.put("/api/v1/predictions/payment/settings", json={"minRows": 200}, headers=h(duties="collections.view,org.switcher.all-scope")).status_code == 403
    # The tenant's min_rows now applies: 250 paid invoices train a model for A, not for B.
    assert client.post("/api/v1/predictions/payment/retrain", json=paid_body(250), headers=h()).status_code == 201
    assert client.post("/api/v1/predictions/payment/retrain", json=paid_body(250), headers=h(TENANT_B)).status_code == 422


def test_accuracy_report_reads_predictions_whose_invoices_were_paid(client):
    assert client.post("/api/v1/predictions/payment/retrain", json=paid_body(600), headers=h()).status_code == 201
    opened = open_body(2)
    pred = client.post("/api/v1/predictions/payment/score", json=opened, headers=h()).json()["items"][0]
    empty = client.get("/api/v1/predictions/payment/accuracy", headers=h())
    assert empty.status_code == 200 and empty.json()["count"] == 0 and empty.json()["maeDays"] is None
    paid = paid_body(600)
    first = opened["openInvoices"][0]
    paid["paidInvoices"].append({**first, "paidDate": pred["predictedPaymentDate"]})
    assert client.post("/api/v1/predictions/payment/retrain", json=paid, headers=h()).status_code == 201
    report = client.get("/api/v1/predictions/payment/accuracy", headers=h()).json()
    assert report["count"] == 1 and report["maeDays"] == "0.00" and report["withinShare"] == "1.0000"
    assert client.get("/api/v1/predictions/payment/accuracy", headers=h(TENANT_B)).json()["count"] == 0
    assert client.get("/api/v1/predictions/payment/accuracy", headers=h(duties="collections.view,org.switcher.all-scope")).status_code == 403


LE = "44444444-4444-4444-4444-444444444444"


def test_the_gateway_routes_are_the_same_handlers_under_v1(client):
    """The gateway rewrites /api/v1/predictions to /v1/predictions (deploy/gateway), so both prefixes answer."""
    assert client.post("/v1/predictions/payment/retrain", json=paid_body(600), headers=h()).status_code == 201
    scored = client.post("/v1/predictions/payment/score", json=open_body(2), headers=h()).json()["items"][0]
    g = client.get(f"/v1/ar/invoices/{scored['invoiceId']}/prediction", headers=h(duties="collections.view,org.switcher.all-scope"))
    assert g.status_code == 200 and g.json()["invoiceId"] == scored["invoiceId"]
    assert client.get("/v1/predictions/payment/accuracy", headers=h()).status_code == 200


def test_the_dso_tile_reads_the_forecast_of_a_legal_entity(client):
    assert client.get(f"/v1/predictions/payment/dso?legalEntityId={LE}", headers=h(duties="agents.admin")).status_code == 403
    assert client.get("/v1/predictions/payment/dso", headers=h(duties="collections.view,org.switcher.all-scope")).status_code == 400
    assert client.get(f"/v1/predictions/payment/dso?legalEntityId={LE}", headers=h(duties="collections.view", entity=LE)).status_code == 404
    client.post("/v1/predictions/payment/retrain", json=paid_body(600), headers=h())
    invoices = synth.open_invoices(25)
    client.post("/v1/predictions/payment/score", json=open_body(25), headers=h())
    api.service.repo.save_invoice_facts(TENANT_A, LE, invoices)
    got = client.get(f"/v1/predictions/payment/dso?legalEntityId={LE}", headers=h(duties="collections.view", entity=LE))
    assert got.status_code == 200, got.text
    body = got.json()
    assert body["legalEntityId"] == LE and body["openInvoices"] == 25 and isinstance(body["dsoDays"], float) and body["stale"] is False
    assert 0 <= float(body["avgPLate30d"]) <= 1
    # Another tenant sees nothing of it.
    assert client.get(f"/v1/predictions/payment/dso?legalEntityId={LE}", headers=h(tenant=TENANT_B, duties="collections.view", entity=LE)).status_code == 404


def test_the_dso_of_another_legal_entity_is_not_there_for_a_scoped_caller(client):
    other = "55555555-5555-4555-8555-555555555555"
    client.post("/v1/predictions/payment/retrain", json=paid_body(600), headers=h())
    client.post("/v1/predictions/payment/score", json=open_body(10), headers=h())
    api.service.repo.save_invoice_facts(TENANT_A, LE, synth.open_invoices(10))
    url = f"/v1/predictions/payment/dso?legalEntityId={LE}"
    assert client.get(url, headers=h(duties="collections.view", entity=LE)).status_code == 200
    assert client.get(url, headers=h(duties="collections.view", entity=other)).status_code == 404
    assert client.get(url, headers=h(duties="collections.view", entity=f"{other},{LE}")).status_code == 200
    # No scope claim: only a caller who holds the all-scope duty may name an entity.
    assert client.get(url, headers=h(duties="collections.view")).status_code == 404
    assert client.get(url, headers=h(duties="collections.view,org.switcher.all-scope")).status_code == 200


def test_a_scoped_caller_scores_and_reads_only_its_own_legal_entities(client):
    other = "00000000-0000-4000-8000-0000000000b2"
    assert client.post("/api/v1/predictions/payment/retrain", json=paid_body(600), headers=h()).status_code == 201
    # A caller scoped to one entity stores for it without naming it; the prediction is there for it and for an all-scope caller only.
    scored = client.post("/api/v1/predictions/payment/score", json=open_body(3), headers=h(entity=LE)).json()["items"][0]
    url = f"/api/v1/ar/invoices/{scored['invoiceId']}/prediction"
    assert client.get(url, headers=h(entity=LE)).status_code == 200
    assert client.get(url, headers=h()).status_code == 200
    assert client.get(url, headers=h(entity=other)).status_code == 404
    # Neither a legal entity nor the all-scope duty: nothing of a legal-entity table is visible.
    assert client.get(url, headers=h(duties="collections.view")).status_code == 404
    # A caller scoped to several entities names one; naming one outside its scope is not there for it; storing over another entity's invoice is refused.
    both = f"{LE},{other}"
    body = {k: v for k, v in open_body(2).items() if k != "legalEntityId"}
    assert client.post("/api/v1/predictions/payment/score", json=body, headers=h(entity=both)).status_code == 400
    assert client.post("/api/v1/predictions/payment/score", json=body, headers=h()).status_code == 400  # the all-scope duty does not excuse it
    assert client.post("/api/v1/predictions/payment/score", json={**body, "legalEntityId": "00000000-0000-4000-8000-0000000000c3"}, headers=h(entity=both)).status_code == 404
    assert client.post("/api/v1/predictions/payment/score", json={**body, "legalEntityId": other}, headers=h(entity=both)).status_code == 200
    stolen = client.post("/api/v1/predictions/payment/score", json={**open_body(3), "legalEntityId": other}, headers=h(entity=other))
    assert stolen.status_code == 403 and stolen.json()["code"] == "PRD-0403"
    # The accuracy report counts what the caller can see.
    assert client.get("/api/v1/predictions/payment/accuracy", headers=h(entity=other)).status_code == 200
