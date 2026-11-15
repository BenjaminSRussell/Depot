"""The feed from receivables (#47, BR-47.12): the weekly retrain on the paid invoices and the delta recompute of the open ones.

weekly: reads every paid invoice of the tenant's legal entities, retrains (the model becomes ACTIVE or stays CANDIDATE by the MAE gate),
then scores every open invoice. delta: reads the paid invoices changed since the last read (their predictions get the actual paid
date, which the accuracy report reads) and the open invoices changed since the last read (a settlement, a dunning step or a dispute
changed the features) and rescores just those; a legal entity with no read yet is read in full. Both keep their watermark per legal
entity only after the pass completed, so a failed read is read again, never skipped. Run as `python -m prediction.sync weekly|delta`
(chart CronJobs), the scope being PREDICTION_SYNC_SCOPES, a JSON list of {"tenantId": ..., "legalEntityIds": [...]}.
"""

from __future__ import annotations

import json
import logging
import os
import sys
from dataclasses import dataclass, field, replace
from datetime import datetime

from prediction.domain import OpenInvoice, PaidInvoice, PredictionError
from prediction.receivables import FeedError, ReceivablesFeed
from prediction.service import PredictionService, SyncState
from prediction.signer import ServiceSigner

log = logging.getLogger("prediction.sync")
SCORE_BATCH = 5_000
MAX_TRAINING_ROWS = 50_000


@dataclass
class Report:
    tenant_id: str
    retrained: str = ""
    scored: int = 0
    actuals: int = 0
    skipped: str = ""
    errors: list[str] = field(default_factory=list)


def _score_all(service: PredictionService, tenant_id: str, legal_entity_id: str, invoices: list[OpenInvoice]) -> int:
    n = 0
    for i in range(0, len(invoices), SCORE_BATCH):
        batch = invoices[i : i + SCORE_BATCH]
        service.score(tenant_id, batch)
        service.repo.save_invoice_facts(tenant_id, legal_entity_id, batch)
        n += len(batch)
    return n


def _read(feed: ReceivablesFeed, tenant_id: str, legal_entity_id: str, state: str, since: datetime | None) -> tuple[list[OpenInvoice], datetime | None]:
    out: list[OpenInvoice] = []
    last = since
    for page in feed.read(tenant_id, legal_entity_id, state, since):
        out.extend(page.invoices)
        last = page.updated_at or last
    return out, last


def weekly(service: PredictionService, feed: ReceivablesFeed, tenant_id: str, legal_entity_ids: list[str]) -> Report:
    """Retrains the tenant's model on all paid invoices and rescores all open ones."""
    rep = Report(tenant_id)
    paid: list[PaidInvoice] = []
    paid_marks: dict[str, datetime | None] = {}
    try:
        for le in legal_entity_ids:
            invoices, paid_marks[le] = _read(feed, tenant_id, le, "paid", None)
            paid.extend(i for i in invoices if isinstance(i, PaidInvoice))
    except FeedError as exc:
        rep.errors.append(str(exc))
        return rep
    # The most recent history, as the retrain route takes at most this many.
    paid = sorted(paid, key=lambda i: i.paid_date)[-MAX_TRAINING_ROWS:]
    try:
        model = service.retrain(tenant_id, paid)
        rep.retrained = f"{model.model_version} {model.status}"
    except PredictionError as exc:
        rep.skipped = f"{exc.code}: {exc}"
    now = service.clock()
    for le in legal_entity_ids:
        try:
            state = service.repo.sync_state(tenant_id, le)
            if service.active_model(tenant_id) is None:
                service.repo.save_sync_state(tenant_id, le, replace(state, paid_watermark=paid_marks.get(le), last_retrain_at=now))
                continue
            opened, open_mark = _read(feed, tenant_id, le, "open", None)
            rep.scored += _score_all(service, tenant_id, le, opened)
            service.repo.save_sync_state(tenant_id, le, SyncState(open_mark, paid_marks.get(le), now))
        except FeedError as exc:
            rep.errors.append(str(exc))
    return rep


def delta(service: PredictionService, feed: ReceivablesFeed, tenant_id: str, legal_entity_ids: list[str]) -> Report:
    """Rescores the open invoices that changed since the last read and stamps the paid date on the ones that were paid."""
    rep = Report(tenant_id)
    for le in legal_entity_ids:
        try:
            state = service.repo.sync_state(tenant_id, le)
            paid, paid_mark = _read(feed, tenant_id, le, "paid", state.paid_watermark)
            rep.actuals += service.repo.record_actuals(tenant_id, [p for p in paid if isinstance(p, PaidInvoice)])
            if service.active_model(tenant_id) is None:
                rep.skipped = "no active model: the weekly retrain has not produced one yet"
                service.repo.save_sync_state(tenant_id, le, replace(state, paid_watermark=paid_mark))
                continue
            opened, open_mark = _read(feed, tenant_id, le, "open", state.open_watermark)
            rep.scored += _score_all(service, tenant_id, le, opened)
            service.repo.save_sync_state(tenant_id, le, SyncState(open_mark, paid_mark, state.last_retrain_at))
        except FeedError as exc:
            rep.errors.append(str(exc))
    return rep


def scopes_from_env(raw: str) -> list[tuple[str, list[str]]]:
    try:
        data = json.loads(raw or "[]")
        return [(str(s["tenantId"]), [str(x) for x in s["legalEntityIds"]]) for s in data]
    except (ValueError, KeyError, TypeError) as exc:
        raise ValueError('PREDICTION_SYNC_SCOPES must be a JSON list of {"tenantId": ..., "legalEntityIds": [...]}') from exc


def main(argv: list[str], getenv=os.environ.get) -> int:  # type: ignore[no-untyped-def]
    if len(argv) != 2 or argv[1] not in ("weekly", "delta"):
        print("usage: python -m prediction.sync weekly|delta", file=sys.stderr)
        return 2
    from prediction.runtime import config_from_env, repository_from_env

    url, signer = getenv("RECEIVABLES_URL"), ServiceSigner.from_env(getenv)
    if not url or signer is None:
        print("RECEIVABLES_URL and a service signing key (PASSAGE_SERVICE_NAME=prediction, PASSAGE_SERVICE_SIGNING_KEY) are needed", file=sys.stderr)
        return 2
    scopes = scopes_from_env(getenv("PREDICTION_SYNC_SCOPES") or "")
    service, feed = PredictionService(repository_from_env(), config_from_env()), ReceivablesFeed(url, signer)
    job = weekly if argv[1] == "weekly" else delta
    failed = False
    for tenant_id, entities in scopes:
        rep = job(service, feed, tenant_id, entities)
        log.info("prediction sync %s", argv[1], extra={"tenantId": tenant_id, "retrained": rep.retrained, "scored": rep.scored, "actuals": rep.actuals, "skipped": rep.skipped})
        print(json.dumps({"job": argv[1], "tenantId": tenant_id, "retrained": rep.retrained, "scored": rep.scored, "actuals": rep.actuals, "skipped": rep.skipped, "errors": rep.errors}))
        failed = failed or bool(rep.errors)
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
