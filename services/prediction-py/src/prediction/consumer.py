"""Recompute on receivables events (#47, BR-47.12): consumes the receivables invoice, payment and settlement events and rescores the
invoices they touch, so a payment, a credit settlement, a dispute or a dunning letter moves the prediction within seconds and not at the
next 15-minute delta.

Each event names a tenant, a legal entity and the open items it changed (`open_item_id`s in the data). The recompute is the sync delta
for that legal entity (prediction.sync.delta): the feed returns the invoices changed since the entity's watermark, which includes the ones
the event names, and their predictions are scored again. Events of one batch for the same entity share one pass. Each event is recorded
in `consumer_inbox` in the transaction around its recompute, so a redelivered event is not handled twice and a recompute that failed
leaves no record and is delivered again; offsets are committed only after the batch was handled. The row and the recompute are not one
transaction (the recompute writes through its own idempotent upserts), so a crash in between repeats the recompute: delivery is at least
once. A recompute that can never succeed (receivables refuses the request with a 4xx, the data is malformed) is recorded in the inbox
as failed, logged and counted, and skipped, so one entity cannot block a partition; a transient failure (5xx, timeout) is retried with
backoff. The 15-minute CronJob stays as the safety
net for an event that never arrived. Run as `python -m prediction.consumer` (chart `consumer`, off by default).
"""

from __future__ import annotations

import json
import logging
import os
import signal
import sys
import time
import uuid
from collections.abc import Callable, Iterable
from dataclasses import dataclass, field
from typing import Any, Protocol

from prediction.service import PredictionService
from prediction.receivables import ReceivablesFeed
from prediction.signer import ServiceSigner
from prediction.sync import Report, delta

log = logging.getLogger("prediction.consumer")

CONSUMER = "prediction-receivables"
TYPE_PREFIX = "dev.passage.receivables."
# The receivables topics whose events change an invoice's open amount, dunning level or disputes (receivables events/asyncapi.yaml).
DEFAULT_TOPICS = (
    "passage.receivables.payment",
    "passage.receivables.creditsettlement",
    "passage.receivables.dispute",
    "passage.receivables.dunningletter",
    "passage.receivables.deduction",
    "passage.receivables.downpayment",
    "passage.receivables.openitem",
)
HANDLED_TYPES = frozenset(
    TYPE_PREFIX + t
    for t in (
        "payment.received.v1",
        "payment.applied.v1",
        "payment.unapplied.v1",
        "creditsettlement.applied.v1",
        "creditsettlement.reversed.v1",
        "dispute.opened.v1",
        "dispute.resolved.v1",
        "dunningletter.issued.v1",
        "deduction.opened.v1",
        "deduction.resolved.v1",
        "downpayment.cleared.v1",
        "downpayment.released.v1",
        "openitem.imported.v1",
    )
)
ITEM_KEYS = ("open_item_id", "credit_item_id")


class MalformedEvent(ValueError):
    """The message is not a usable event; it is logged and skipped (redelivery would not make it usable)."""


class RecomputeFailed(Exception):
    """A recompute failed in a way that may pass (receivables down); the batch is not committed and its events are delivered again."""


# Counters of this process, logged with every event they count (there is no metrics port in this worker; the log line carries `metric`).
METRICS: dict[str, int] = {"prediction_consumer_permanent_failures_total": 0}


@dataclass(frozen=True)
class Record:
    """One broker message."""

    topic: str
    partition: int
    offset: int
    value: bytes
    headers: dict[str, str] = field(default_factory=dict)


@dataclass(frozen=True)
class ReceivablesEvent:
    event_id: str
    type: str
    tenant_id: str
    legal_entity_id: str
    invoice_ids: tuple[str, ...]


def _canonical(value: Any, what: str) -> str:
    try:
        return str(uuid.UUID(str(value)))
    except (ValueError, AttributeError, TypeError) as exc:
        raise MalformedEvent(f"{what} is not a UUID") from exc


def _items(node: Any, out: dict[str, None]) -> None:
    """The ids under the data's item keys, wherever they sit (settlements, debits, items): the open items the event changed."""
    if isinstance(node, dict):
        for k, v in node.items():
            if k in ITEM_KEYS and isinstance(v, str):
                try:
                    out[str(uuid.UUID(v))] = None
                except ValueError:
                    pass
            else:
                _items(v, out)
    elif isinstance(node, list):
        for v in node:
            _items(v, out)


def decode(record: Record) -> ReceivablesEvent | None:
    """The event in a message: None for a type this consumer does not handle, MalformedEvent for a message that is not a CloudEvent (structured
    JSON, or binary mode with ce_ headers as platform/go/eventing.Decode reads) with a tenant and a legal entity."""
    h = record.headers
    try:
        if "ce_specversion" in h:
            env: dict[str, Any] = {k[3:]: v for k, v in h.items() if k.startswith("ce_")}
            data = json.loads(record.value)
        else:
            env = json.loads(record.value)
            data = env.get("data") if isinstance(env, dict) else None
    except (ValueError, TypeError) as exc:
        raise MalformedEvent(f"not JSON: {exc}") from exc
    if not isinstance(env, dict) or env.get("specversion") != "1.0" or not env.get("id") or not isinstance(env.get("type"), str):
        raise MalformedEvent("not a CloudEvents 1.0 event with an id and a type")
    if env["type"] not in HANDLED_TYPES:
        return None
    if not isinstance(data, dict):
        raise MalformedEvent("the event has no data")
    tenant = _canonical(env.get("tenantid"), "tenantid")
    if h.get("tenantid") and _canonical(h["tenantid"], "tenantid header") != tenant:
        raise MalformedEvent("the tenant header disagrees with the event")
    entity = _canonical(env.get("legalentityid") or data.get("legal_entity_id") or data.get("legalEntityId"), "legal entity id")
    ids: dict[str, None] = {}
    _items(data, ids)
    return ReceivablesEvent(str(env["id"]), env["type"], tenant, entity, tuple(ids))


@dataclass
class BatchReport:
    handled: int = 0
    duplicates: int = 0
    skipped: int = 0
    recomputes: int = 0
    failed: int = 0
    rescored: int = 0
    invoices: set[str] = field(default_factory=set)


class Consumer:
    """Handles decoded events. `recompute` is the delta pass for one (tenant, legal entity) and returns its report."""

    def __init__(self, service: PredictionService, feed: ReceivablesFeed, recompute: Callable[[PredictionService, ReceivablesFeed, str, list[str]], Report] = delta) -> None:
        self.service, self.feed, self._recompute = service, feed, recompute

    def process(self, events: Iterable[ReceivablesEvent]) -> BatchReport:
        rep = BatchReport()
        done: set[tuple[str, str]] = set()
        failed: dict[tuple[str, str], str] = {}
        repo = self.service.repo
        for ev in events:
            reason: str | None = None
            with repo.inbox_once(ev.tenant_id, CONSUMER, ev.event_id) as first:
                if not first:
                    rep.duplicates += 1
                    continue
                scope = (ev.tenant_id, ev.legal_entity_id)
                # Everything an event of this batch changed was committed in receivables before the event was published, so one pass for
                # the entity covers all of the batch's events for it.
                if scope in failed:
                    reason = failed[scope]
                elif scope not in done:
                    out = self._recompute(self.service, self.feed, ev.tenant_id, [ev.legal_entity_id])
                    if out.errors and len(out.permanent) < len(out.errors):
                        raise RecomputeFailed("; ".join(out.errors))
                    if out.errors:
                        reason = failed[scope] = "; ".join(out.permanent)
                    else:
                        done.add(scope)
                        rep.recomputes += 1
                        rep.rescored += out.scored
                if reason is None:
                    rep.handled += 1
                    rep.invoices.update(ev.invoice_ids)
            if reason is not None:
                # A recompute that cannot succeed is not retried: it is recorded as failed, counted and skipped, and the 15-minute delta
                # (or the next change of the entity) covers it.
                repo.inbox_outcome(ev.tenant_id, CONSUMER, ev.event_id, "failed", reason)
                METRICS["prediction_consumer_permanent_failures_total"] += 1
                rep.failed += 1
                log.error("receivables event skipped, its recompute cannot succeed: %s", reason, extra={"metric": "prediction_consumer_permanent_failures_total", "event_id": ev.event_id, "tenantId": ev.tenant_id, "legalEntityId": ev.legal_entity_id})
        return rep


class Source(Protocol):
    def poll(self, timeout_ms: int) -> list[Record]: ...
    def commit(self) -> None: ...
    def rewind(self) -> None: ...
    def close(self) -> None: ...


def run_once(source: Source, consumer: Consumer, timeout_ms: int = 1000) -> BatchReport | None:
    """One poll: decode, handle, commit. A malformed message is logged and skipped; a failed batch rewinds to the last commit (so it is read
    again) and raises RecomputeFailed for the caller to back off. Returns None when the poll was empty."""
    records = source.poll(timeout_ms)
    if not records:
        return None
    events: list[ReceivablesEvent] = []
    skipped = 0
    for r in records:
        try:
            ev = decode(r)
        except MalformedEvent as exc:
            log.error("receivables event skipped: %s", exc, extra={"topic": r.topic, "partition": r.partition, "offset": r.offset})
            skipped += 1
            continue
        if ev is None:
            skipped += 1
        else:
            events.append(ev)
    try:
        rep = consumer.process(events)
    except Exception as exc:  # noqa: BLE001 - any failure leaves the batch uncommitted and delivered again
        log.error("receivables events not handled, delivered again: %s", exc)
        source.rewind()
        raise RecomputeFailed(str(exc)) from exc
    rep.skipped = skipped
    source.commit()
    log.info("receivables events handled", extra={"handled": rep.handled, "duplicates": rep.duplicates, "skipped": rep.skipped, "failed": rep.failed, "recomputes": rep.recomputes, "rescored": rep.rescored, "invoices": len(rep.invoices)})
    return rep


class KafkaSource:
    """kafka-python consumer in a group, manual commit after a batch is handled, so a delivery is at least once."""

    def __init__(self, brokers: str, group: str, topics: Iterable[str], max_records: int = 200, consumer: Any = None, security: dict[str, Any] | None = None) -> None:
        if consumer is None:
            from kafka import KafkaConsumer  # imported here: the HTTP service and the sync jobs do not need it

            consumer = KafkaConsumer(*topics, bootstrap_servers=brokers.split(","), group_id=group, enable_auto_commit=False, auto_offset_reset="earliest", max_poll_records=max_records, **(security or {}))
        self._c = consumer

    def poll(self, timeout_ms: int) -> list[Record]:
        out: list[Record] = []
        for batch in self._c.poll(timeout_ms=timeout_ms).values():
            for m in batch:
                headers = {k: v.decode("utf-8", "replace") if isinstance(v, bytes) else str(v) for k, v in (m.headers or [])}
                out.append(Record(m.topic, m.partition, m.offset, m.value or b"", headers))
        return out

    def commit(self) -> None:
        self._c.commit()

    def rewind(self) -> None:
        """Back to the last committed offset of every assigned partition, so an uncommitted batch is read again."""
        for tp in self._c.assignment():
            committed = self._c.committed(tp)
            if committed is None:
                self._c.seek_to_beginning(tp)
            else:
                self._c.seek(tp, committed)

    def close(self) -> None:
        self._c.close()


def kafka_security(getenv: Callable[[str], str | None], read: Callable[[str], str] | None = None) -> dict[str, Any]:
    """The consumer's connection settings as the cluster requires them (deploy/kafka/kafka.yaml): SCRAM-SHA-512 over TLS for the KafkaUser
    (KAFKA_USERNAME, KAFKA_PASSWORD_FILE) and the cluster CA (KAFKA_TLS_CA_FILE), the names workflow-ts's consumer reads. Nothing set means
    the plain listener (dev)."""
    user = getenv("KAFKA_USERNAME")
    if not user:
        return {}
    reader = read or (lambda p: open(p, encoding="utf-8").read())
    password_file = getenv("KAFKA_PASSWORD_FILE")
    if not password_file:
        raise ValueError("KAFKA_USERNAME needs KAFKA_PASSWORD_FILE")
    out: dict[str, Any] = {"security_protocol": "SASL_SSL", "sasl_mechanism": "SCRAM-SHA-512", "sasl_plain_username": user, "sasl_plain_password": reader(password_file).strip()}
    if getenv("KAFKA_TLS_CA_FILE"):
        out["ssl_cafile"] = getenv("KAFKA_TLS_CA_FILE")
    return out


def main(argv: list[str], getenv=os.environ.get) -> int:  # type: ignore[no-untyped-def]
    brokers, url, signer = getenv("KAFKA_BROKERS"), getenv("RECEIVABLES_URL"), ServiceSigner.from_env(getenv)
    if not brokers or not url or signer is None:
        print("KAFKA_BROKERS, RECEIVABLES_URL and a service signing key (PASSAGE_SERVICE_NAME=prediction, PASSAGE_SERVICE_SIGNING_KEY) are needed", file=sys.stderr)
        return 2
    from prediction.runtime import config_from_env, repository_from_env

    topics = [t.strip() for t in (getenv("PREDICTION_KAFKA_TOPICS") or ",".join(DEFAULT_TOPICS)).split(",") if t.strip()]
    consumer = Consumer(PredictionService(repository_from_env(), config_from_env()), ReceivablesFeed(url, signer))
    source = KafkaSource(brokers, getenv("PREDICTION_KAFKA_GROUP") or "prediction", topics, security=kafka_security(getenv))
    stop = {"now": False}
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: stop.update(now=True))
    backoff = 1.0
    try:
        while not stop["now"]:
            try:
                run_once(source, consumer)
                backoff = 1.0
            except RecomputeFailed:
                # The batch is read again after a pause that grows to 30 s, so an outage of receivables is not hammered.
                time.sleep(backoff)
                backoff = min(backoff * 2, 30.0)
    finally:
        source.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
