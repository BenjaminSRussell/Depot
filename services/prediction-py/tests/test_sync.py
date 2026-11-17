"""The feed from receivables: the service token the client signs, the paged read, the weekly retrain and the delta recompute."""

from __future__ import annotations

import base64
from pathlib import Path
import json
import uuid
from datetime import date, datetime, timedelta, timezone

import httpx
import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from prediction import synth
from prediction.domain import Config, OpenInvoice, PaidInvoice
from prediction.receivables import FeedError, ReceivablesFeed
from prediction.service import InMemoryRepository, PredictionService
from prediction.signer import GRANTS, HEADER, TOKEN_CONTEXT, ServiceSigner, SignerError
from prediction.sync import OVERLAP, delta, scopes_from_env, weekly

TENANT = "0190a5f2-7b1c-7d3e-8a4f-1234567890ab"
LE = "0190a5f2-7b1c-7d3e-8a4f-1234567890ac"
LE2 = "0190a5f2-7b1c-7d3e-8a4f-1234567890ad"
NOW = datetime(2026, 11, 13, 10, 0, tzinfo=timezone.utc)
KEY = Ed25519PrivateKey.from_private_bytes(bytes(range(1, 33)))
SEED = base64.b64encode(bytes(range(1, 33))).decode()


def b64d(s: str) -> bytes:
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def test_a_minted_token_carries_one_grant_duty_and_verifies_with_the_public_key():
    signer = ServiceSigner(KEY, kid="k1", now=lambda: 1_793_275_200)
    token = signer.mint("receivables", TENANT, LE, ["receivables.prediction.read"])
    body, sig = token.split(".")
    KEY.public_key().verify(b64d(sig), TOKEN_CONTEXT + body.encode())
    claims = json.loads(b64d(body))
    assert claims["iss"] == "prediction" and claims["aud"] == "receivables" and claims["tid"] == TENANT and claims["le"] == LE
    assert claims["act"] == "service:prediction-sync" and claims["duties"] == ["receivables.prediction.read"] and claims["exp"] - claims["iat"] == 60
    # The Go verifier accepts exactly this wire format: platform/go/tenant testdata/prediction_py_token_vector.json is minted here.
    vector = json.load(open(Path(__file__).resolve().parents[3] / "platform/go/tenant/testdata/prediction_py_token_vector.json", encoding="utf-8"))
    uuid_fixed = uuid.UUID("00112233-4455-6677-8899-aabbccddeeff")
    real = uuid.uuid4
    uuid.uuid4 = lambda: uuid_fixed  # type: ignore[assignment]
    try:
        again = ServiceSigner(KEY, kid=vector["keyId"], now=lambda: vector["iat"]).mint("receivables", vector["tenantId"], vector["legalEntityId"], ["receivables.prediction.read"])
    finally:
        uuid.uuid4 = real  # type: ignore[assignment]
    assert again == vector["token"]


def test_the_signer_refuses_what_the_grant_does_not_cover():
    signer = ServiceSigner(KEY)
    for aud, duties in (("ledger", ["receivables.prediction.read"]), ("receivables", ["receivables.view"]), ("receivables", [])):
        with pytest.raises(SignerError):
            signer.mint(aud, TENANT, LE, duties)
    with pytest.raises(SignerError):
        signer.mint("receivables", "not-a-uuid" if False else str(uuid.UUID(int=0)), LE, ["receivables.prediction.read"])
    assert GRANTS == {"receivables": ("receivables.prediction.read",)}


def test_the_signer_reads_its_key_from_the_environment():
    env = {"DEPOT_SERVICE_SIGNING_KEY": SEED, "DEPOT_SERVICE_NAME": "prediction", "DEPOT_SERVICE_KEY_ID": "k1"}
    signer = ServiceSigner.from_env(env.get)
    assert signer is not None and signer.mint("receivables", TENANT, LE, ["receivables.prediction.read"])
    assert ServiceSigner.from_env({}.get) is None
    with pytest.raises(SignerError):
        ServiceSigner.from_env({**env, "DEPOT_SERVICE_NAME": "ledger"}.get)
    with pytest.raises(SignerError):
        ServiceSigner.from_env({"DEPOT_SERVICE_SIGNING_KEY": "AAAA"}.get)


class FakeReceivables:
    """GET /v1/receivables/prediction-invoices over invoices with a change time, paged by 2 or 500, as the service answers."""

    def __init__(self, page: int = 500) -> None:
        self.rows: dict[str, list[tuple[datetime, dict]]] = {"open": [], "paid": [], "reversed": []}
        self.calls: list[dict] = []
        self.fail: int | None = None
        self.page = page

    def add(self, entity: str, state: str, inv: OpenInvoice, at: datetime) -> None:
        row = {"id": inv.invoice_id, "legalEntityId": entity, "customerId": inv.customer_id, "currency": "USD", "amount": f"{inv.amount:.2f}", "documentDate": inv.invoice_date.isoformat(),
               "dueDate": inv.due_date.isoformat(), "dunningLevel": inv.dunning_count, "disputes": inv.disputes, "updatedAt": at.isoformat()}
        if state == "paid":
            row["paidDate"] = inv.paid_date.isoformat()  # type: ignore[attr-defined]
        self.rows[state].append((at, row))

    def handler(self, request: httpx.Request) -> httpx.Response:
        q = dict(request.url.params)
        token = request.headers[HEADER]
        claims = json.loads(b64d(token.split(".")[0]))
        self.calls.append({"query": q, "claims": claims})
        if self.fail:
            return httpx.Response(self.fail, json={"code": "COM-0500"})
        since = datetime.fromisoformat(q["updated_since"]) if "updated_since" in q else None
        rows = sorted((at, r) for at, r in self.rows[q["state"]] if r["legalEntityId"] == claims["le"] and (since is None or at >= since))
        start = int(q["cursor"]) if "cursor" in q else 0
        page = rows[start : start + self.page]
        body: dict = {"items": [r for _, r in page], "asOf": NOW.isoformat()}
        if start + self.page < len(rows):
            body["nextCursor"] = str(start + self.page)
        return httpx.Response(200, json=body)


def setup(page: int = 500, n_paid: int = 400, n_open: int = 30):
    fake = FakeReceivables(page)
    client = httpx.Client(transport=httpx.MockTransport(fake.handler))
    feed = ReceivablesFeed("http://receivables", ServiceSigner(KEY), client)
    repo = InMemoryRepository()
    service = PredictionService(repo, Config(min_rows=100, mae_max_days=30.0), clock=lambda: NOW)
    base = datetime(2026, 11, 1, tzinfo=timezone.utc)
    for i, inv in enumerate(synth.paid_invoices(n_paid)):
        fake.add(LE, "paid", inv, base + timedelta(hours=i))
    for i, inv in enumerate(synth.open_invoices(n_open)):
        fake.add(LE, "open", inv, base + timedelta(hours=i))
    return fake, feed, service, repo


def test_the_weekly_job_retrains_on_the_paid_history_and_scores_every_open_invoice_across_pages():
    fake, feed, service, repo = setup(page=100)
    rep = weekly(service, feed, TENANT, [LE])
    assert not rep.errors and rep.retrained.endswith("ACTIVE") and rep.scored == 30
    assert all(c["claims"]["le"] == LE and c["claims"]["duties"] == ["receivables.prediction.read"] for c in fake.calls)
    assert [c["query"]["state"] for c in fake.calls].count("paid") == 4  # 400 paid invoices in pages of 100
    state = repo.sync_state(TENANT, LE)
    assert state.open_watermark is not None and state.paid_watermark is not None and state.last_retrain_at == NOW
    assert repo.dso(TENANT, LE) is not None and repo.dso(TENANT, LE).open_invoices == 30
    assert service.prediction(TENANT, synth.open_invoices(30)[0].invoice_id) is not None


def test_the_weekly_job_with_too_little_history_trains_nothing_and_scores_nothing():
    _fake, feed, service, _repo = setup(n_paid=20)
    rep = weekly(service, feed, TENANT, [LE])
    assert rep.retrained == "" and rep.scored == 0 and rep.skipped.startswith("AGT-0213") and not rep.errors


def test_the_delta_rescores_only_invoices_changed_since_the_watermark_and_stamps_paid_dates():
    fake, feed, service, repo = setup()
    weekly(service, feed, TENANT, [LE])
    scored = {p for p in [synth.open_invoices(30)[i].invoice_id for i in range(30)]}
    nothing = delta(service, feed, TENANT, [LE])
    # The watermark is inclusive, so the last-changed invoice of each state is read again; that is all.
    assert not nothing.errors and nothing.scored <= 1 and nothing.actuals == 0
    # An open invoice is disputed and another one paid after the last read.
    opened = synth.open_invoices(30)
    later = NOW + timedelta(days=60)
    changed = OpenInvoice(opened[3].invoice_id, opened[3].customer_id, opened[3].amount, opened[3].invoice_date, opened[3].due_date, 2, 1)
    fake.rows["open"] = [(at, r) for at, r in fake.rows["open"] if r["id"] != changed.invoice_id]
    fake.add(LE, "open", changed, later)
    pay = opened[4]
    fake.rows["open"] = [(at, r) for at, r in fake.rows["open"] if r["id"] != pay.invoice_id]
    fake.add(LE, "paid", PaidInvoice(**{**pay.__dict__, "paid_date": pay.due_date}), later)
    before = repo.prediction(TENANT, changed.invoice_id)
    rep = delta(service, feed, TENANT, [LE])
    assert not rep.errors and 1 <= rep.scored <= 2 and rep.actuals == 1
    after = repo.prediction(TENANT, changed.invoice_id)
    assert after.features_hash != before.features_hash  # the dunning level and the dispute changed the features
    assert repo.dso(TENANT, LE).open_invoices == 29 and scored


def test_a_failed_read_keeps_the_watermark_and_is_reported():
    fake, feed, service, repo = setup()
    weekly(service, feed, TENANT, [LE])
    mark = repo.sync_state(TENANT, LE)
    fake.fail = 503
    rep = delta(service, feed, TENANT, [LE])
    assert rep.errors and "503" in rep.errors[0] and rep.scored == 0
    assert repo.sync_state(TENANT, LE) == mark
    with pytest.raises(FeedError):
        list(feed.read(TENANT, LE, "open", None))
    fake.fail = None
    assert not delta(service, feed, TENANT, [LE]).errors


def test_a_legal_entity_without_a_model_yet_only_records_actuals():
    _fake, feed, service, repo = setup(n_paid=20)
    rep = delta(service, feed, TENANT, [LE])
    assert rep.scored == 0 and rep.skipped.startswith("no active model")
    assert repo.sync_state(TENANT, LE).open_watermark is None


def test_scopes_come_from_the_environment_and_a_malformed_one_is_skipped():
    assert scopes_from_env(json.dumps([{"tenantId": TENANT, "legalEntityIds": [LE, LE2]}])) == [(TENANT, [LE, LE2])]
    assert scopes_from_env("") == []
    # Capitals are put in canonical form; a bad scope is skipped and the ones after it still run.
    mixed = [{"tenantId": TENANT.upper(), "legalEntityIds": [LE.upper()]}, {"tenant": 1}, {"tenantId": "x", "legalEntityIds": [LE]}, {"tenantId": TENANT, "legalEntityIds": [LE2]}]
    assert scopes_from_env(json.dumps(mixed)) == [(TENANT, [LE]), (TENANT, [LE2])]
    with pytest.raises(ValueError):
        scopes_from_env("not json")


def test_a_signer_failure_for_one_legal_entity_is_reported_and_the_next_entity_still_runs():
    fake, feed, service, repo = setup()
    weekly(service, feed, TENANT, [LE])
    rep = delta(service, feed, TENANT, ["not-a-uuid", LE])
    assert len(rep.errors) == 1 and rep.errors[0].startswith("not-a-uuid")
    assert repo.sync_state(TENANT, LE).open_watermark is not None


def test_the_delta_reads_from_the_watermark_less_the_overlap_so_a_late_commit_is_not_skipped():
    fake, feed, service, repo = setup()
    weekly(service, feed, TENANT, [LE])
    mark = repo.sync_state(TENANT, LE).open_watermark
    fake.calls.clear()
    delta(service, feed, TENANT, [LE])
    since = [datetime.fromisoformat(c["query"]["updated_since"]) for c in fake.calls if c["query"]["state"] == "open"]
    assert since == [mark - OVERLAP]
    # A row that committed late with a change time before the stored watermark, inside the overlap, is rescored.
    opened = synth.open_invoices(30)[7]
    fake.rows["open"] = [(at, r) for at, r in fake.rows["open"] if r["id"] != opened.invoice_id]
    fake.add(LE, "open", OpenInvoice(opened.invoice_id, opened.customer_id, opened.amount, opened.invoice_date, opened.due_date, 3, 0), mark - timedelta(minutes=3))
    assert delta(service, feed, TENANT, [LE]).scored >= 1


def test_a_reversed_invoice_leaves_the_forecast():
    fake, feed, service, repo = setup()
    weekly(service, feed, TENANT, [LE])
    gone = synth.open_invoices(30)[2]
    fake.rows["open"] = [(at, r) for at, r in fake.rows["open"] if r["id"] != gone.invoice_id]
    fake.add(LE, "reversed", gone, NOW + timedelta(days=60))
    assert repo.dso(TENANT, LE).open_invoices == 30
    delta(service, feed, TENANT, [LE])
    assert repo.dso(TENANT, LE).open_invoices == 29
