"""The receivables event consumer: decoding, dedupe through the inbox, one recompute per entity and batch, commit only after the batch,
and the Kafka adapter over a fake client."""

from __future__ import annotations

import json
import uuid
from datetime import datetime, timedelta, timezone

import pytest
from test_sync import LE, LE2, NOW, TENANT, setup  # noqa: F401 - the fake receivables and its fixtures

from prediction import synth
from prediction.consumer import (
    CONSUMER,
    DEFAULT_TOPICS,
    HANDLED_TYPES,
    Consumer,
    KafkaSource,
    MalformedEvent,
    Record,
    RecomputeFailed,
    decode,
    run_once,
)
from prediction.domain import OpenInvoice
from prediction.sync import Report

T = "dev.depot.receivables."


def event(type_: str, data: dict, *, id_: str | None = None, tenant: str = TENANT, le: str | None = LE, extra: dict | None = None) -> Record:
    env = {"specversion": "1.0", "id": id_ or str(uuid.uuid4()), "source": "receivables", "type": T + type_, "time": NOW.isoformat(), "datacontenttype": "application/json", "tenantid": tenant, "data": data, **(extra or {})}
    if le is not None:
        data.setdefault("legal_entity_id", le)
    return Record("depot.receivables.payment", 0, 1, json.dumps(env).encode())


def applied(items: list[str], id_: str | None = None, le: str = LE) -> Record:
    return event("payment.applied.v1", {"payment_id": str(uuid.uuid4()), "legal_entity_id": le, "settlements": [{"open_item_id": i, "amount": "10"} for i in items]}, id_=id_, le=le)


class Spy:
    """A recompute that records the entities it ran for."""

    def __init__(self, scored: int = 1, errors: list[str] | None = None) -> None:
        self.calls: list[tuple[str, list[str]]] = []
        self.scored, self.errors = scored, errors or []

    def __call__(self, _service, _feed, tenant, entities):  # type: ignore[no-untyped-def]
        self.calls.append((tenant, list(entities)))
        return Report(tenant, scored=self.scored, errors=list(self.errors))


class FakeSource:
    def __init__(self, batches: list[list[Record]]) -> None:
        self.batches, self.commits, self.rewinds, self.closed = batches, 0, 0, False

    def poll(self, timeout_ms: int) -> list[Record]:
        return self.batches.pop(0) if self.batches else []

    def commit(self) -> None:
        self.commits += 1

    def rewind(self) -> None:
        self.rewinds += 1

    def close(self) -> None:
        self.closed = True


def consumer_with(spy: Spy):  # type: ignore[no-untyped-def]
    _fake, feed, service, repo = setup()
    return Consumer(service, feed, spy), repo


def test_decoding_reads_the_tenant_entity_and_every_open_item_the_event_names():
    a, b = str(uuid.uuid4()), str(uuid.uuid4())
    ev = decode(applied([a, b.upper()]))
    assert ev is not None and ev.tenant_id == TENANT and ev.legal_entity_id == LE and ev.type == T + "payment.applied.v1"
    assert set(ev.invoice_ids) == {a, b}
    settled = decode(event("creditsettlement.applied.v1", {"credit_item_id": a, "debits": [{"open_item_id": b, "amount": "1"}]}))
    assert settled is not None and set(settled.invoice_ids) == {a, b}
    assert decode(event("dispute.opened.v1", {"open_item_id": a, "dispute_id": b})).invoice_ids == (a,)  # type: ignore[union-attr]
    assert decode(event("payment.received.v1", {"payment_id": a})).invoice_ids == ()  # type: ignore[union-attr]


def test_decoding_reads_the_entity_from_the_envelope_and_binary_mode_events():
    ev = decode(event("dispute.resolved.v1", {"open_item_id": str(uuid.uuid4())}, le=None, extra={"legalentityid": LE2}))
    assert ev is not None and ev.legal_entity_id == LE2
    data = {"open_item_id": str(uuid.uuid4()), "legal_entity_id": LE}
    headers = {"ce_specversion": "1.0", "ce_id": "e1", "ce_type": T + "dispute.opened.v1", "ce_tenantid": TENANT.upper(), "tenantid": TENANT}
    binary = decode(Record("t", 0, 1, json.dumps(data).encode(), headers))
    assert binary is not None and binary.event_id == "e1" and binary.tenant_id == TENANT


def test_a_type_this_service_does_not_handle_is_skipped_and_a_broken_message_is_malformed():
    assert decode(event("case.opened.v1", {"x": 1})) is None
    assert HANDLED_TYPES >= {T + "payment.applied.v1", T + "creditsettlement.reversed.v1", T + "dispute.opened.v1", T + "dunningletter.issued.v1"}
    assert "depot.receivables.payment" in DEFAULT_TOPICS
    for bad in (b"not json", b"[]", json.dumps({"specversion": "0.3", "id": "x", "type": T + "payment.applied.v1"}).encode(), json.dumps({"specversion": "1.0", "type": T + "payment.applied.v1"}).encode()):
        with pytest.raises(MalformedEvent):
            decode(Record("t", 0, 1, bad))
    with pytest.raises(MalformedEvent):
        decode(event("payment.applied.v1", {"settlements": []}, tenant="not-a-uuid"))
    with pytest.raises(MalformedEvent):
        decode(event("payment.applied.v1", {"settlements": []}, le=None))
    with pytest.raises(MalformedEvent):
        decode(Record("t", 0, 1, json.dumps({"specversion": "1.0", "id": "x", "type": T + "payment.applied.v1", "tenantid": TENANT}).encode()))


def test_a_batch_runs_one_recompute_per_entity_and_records_every_event():
    spy = Spy()
    consumer, repo = consumer_with(spy)
    batch = [decode(r) for r in (applied([str(uuid.uuid4())]), applied([str(uuid.uuid4())]), applied([str(uuid.uuid4())], le=LE2))]
    rep = consumer.process(e for e in batch if e is not None)
    assert spy.calls == [(TENANT, [LE]), (TENANT, [LE2])]
    assert (rep.handled, rep.recomputes, rep.rescored, rep.duplicates, len(rep.invoices)) == (3, 2, 2, 0, 3)
    for e in batch:
        with repo.inbox_once(TENANT, CONSUMER, e.event_id) as first:  # type: ignore[union-attr]
            assert first is False


def test_a_redelivered_event_is_not_handled_again():
    spy = Spy()
    consumer, _repo = consumer_with(spy)
    ev = decode(applied([str(uuid.uuid4())], id_="evt-1"))
    consumer.process([ev])  # type: ignore[list-item]
    again = consumer.process([ev])  # type: ignore[list-item]
    assert len(spy.calls) == 1 and (again.duplicates, again.handled, again.recomputes) == (1, 0, 0)


def test_a_failed_recompute_leaves_no_inbox_record_so_the_event_is_handled_when_delivered_again():
    spy = Spy(errors=["receivables answered 503 for the open invoice feed"])
    consumer, repo = consumer_with(spy)
    ev = decode(applied([str(uuid.uuid4())], id_="evt-2"))
    with pytest.raises(RecomputeFailed):
        consumer.process([ev])  # type: ignore[list-item]
    spy.errors = []
    rep = consumer.process([ev])  # type: ignore[list-item]
    assert rep.handled == 1 and len(spy.calls) == 2


def test_the_inbox_is_per_tenant_and_per_consumer():
    _consumer, repo = consumer_with(Spy())
    other = "0190a5f2-7b1c-7d3e-8a4f-1234567890ff"
    with repo.inbox_once(TENANT, CONSUMER, "e") as a:
        assert a
    with repo.inbox_once(other, CONSUMER, "e") as b, repo.inbox_once(TENANT, "other-consumer", "e") as c:
        assert b and c


def test_an_event_rescores_the_invoice_it_names_through_the_real_delta():
    fake, feed, service, repo = setup()
    from prediction.sync import weekly

    weekly(service, feed, TENANT, [LE])
    opened = synth.open_invoices(30)[3]
    before = repo.prediction(TENANT, opened.invoice_id)
    fake.rows["open"] = [(at, r) for at, r in fake.rows["open"] if r["id"] != opened.invoice_id]
    fake.add(LE, "open", OpenInvoice(opened.invoice_id, opened.customer_id, opened.amount, opened.invoice_date, opened.due_date, 2, 1), NOW + timedelta(days=60))
    rep = Consumer(service, feed).process([decode(applied([opened.invoice_id]))])  # type: ignore[list-item]
    assert rep.handled == 1 and rep.rescored >= 1
    assert repo.prediction(TENANT, opened.invoice_id).features_hash != before.features_hash


def test_run_once_commits_after_the_batch_skips_what_it_cannot_use_and_ignores_an_empty_poll():
    spy = Spy()
    consumer, _repo = consumer_with(spy)
    source = FakeSource([[applied([str(uuid.uuid4())]), Record("t", 0, 2, b"garbage"), event("case.opened.v1", {})], []])
    rep = run_once(source, consumer)
    assert rep is not None and (rep.handled, rep.skipped) == (1, 2) and source.commits == 1 and source.rewinds == 0
    assert run_once(source, consumer) is None and source.commits == 1


def test_run_once_rewinds_without_committing_when_the_recompute_fails():
    consumer, _repo = consumer_with(Spy(errors=["boom"]))
    source = FakeSource([[applied([str(uuid.uuid4())])]])
    with pytest.raises(RecomputeFailed):
        run_once(source, consumer)
    assert source.commits == 0 and source.rewinds == 1


class FakeKafka:
    """The parts of kafka-python's KafkaConsumer the adapter uses."""

    class Msg:
        def __init__(self, topic: str, partition: int, offset: int, value: bytes, headers: list) -> None:  # type: ignore[type-arg]
            self.topic, self.partition, self.offset, self.value, self.headers = topic, partition, offset, value, headers

    def __init__(self) -> None:
        self.assigned = ["tp0", "tp1"]
        self.committed_offsets: dict[str, int | None] = {"tp0": 7, "tp1": None}
        self.calls: list[tuple] = []  # type: ignore[type-arg]

    def poll(self, timeout_ms: int):  # type: ignore[no-untyped-def]
        self.calls.append(("poll", timeout_ms))
        return {"tp0": [self.Msg("t", 0, 7, b"{}", [("ce_id", b"e1"), ("x", "y")])]}

    def commit(self) -> None:
        self.calls.append(("commit",))

    def assignment(self):  # type: ignore[no-untyped-def]
        return self.assigned

    def committed(self, tp: str):  # type: ignore[no-untyped-def]
        return self.committed_offsets[tp]

    def seek(self, tp: str, offset: int) -> None:
        self.calls.append(("seek", tp, offset))

    def seek_to_beginning(self, tp: str) -> None:
        self.calls.append(("beginning", tp))

    def close(self) -> None:
        self.calls.append(("close",))


def test_the_kafka_adapter_turns_messages_into_records_commits_manually_and_rewinds_to_the_committed_offsets():
    k = FakeKafka()
    src = KafkaSource("b:9092", "prediction", DEFAULT_TOPICS, consumer=k)
    assert src.poll(500) == [Record("t", 0, 7, b"{}", {"ce_id": "e1", "x": "y"})]
    src.commit()
    src.rewind()
    src.close()
    assert k.calls == [("poll", 500), ("commit",), ("seek", "tp0", 7), ("beginning", "tp1"), ("close",)]


def test_main_needs_its_environment():
    from prediction.consumer import main

    assert main(["consumer"], {}.get) == 2


class Failing:
    """A recompute that fails permanently for one legal entity and works for the others."""

    def __init__(self, bad: set[str], permanent: bool = True) -> None:
        self.bad, self.permanent, self.calls = bad, permanent, []  # type: ignore[var-annotated]

    def __call__(self, _service, _feed, tenant, entities):  # type: ignore[no-untyped-def]
        self.calls.append(entities[0])
        if entities[0] in self.bad:
            msg = f"{entities[0]}: receivables answered 403 for the open invoice feed"
            return Report(tenant, errors=[msg], permanent=[msg] if self.permanent else [])
        return Report(tenant, scored=1)


def test_an_event_whose_recompute_can_never_succeed_is_recorded_failed_and_skipped_so_it_cannot_block_the_partition():
    from prediction.consumer import METRICS

    fail = Failing({LE})
    consumer, repo = consumer_with(Spy())
    consumer._recompute = fail  # type: ignore[assignment]
    bad, bad2, good = applied([str(uuid.uuid4())], id_="bad-1"), applied([str(uuid.uuid4())], id_="bad-2"), applied([str(uuid.uuid4())], id_="good-1", le=LE2)
    before = METRICS["prediction_consumer_permanent_failures_total"]
    source = FakeSource([[bad, bad2, good]])
    rep = run_once(source, consumer)
    assert rep is not None and (rep.failed, rep.handled, rep.recomputes) == (2, 1, 1)
    assert fail.calls == [LE, LE2]  # one attempt for the bad entity, the healthy one still recomputed
    assert source.commits == 1 and source.rewinds == 0  # the batch moves on
    assert repo.inbox_entry(TENANT, CONSUMER, "bad-1")[0] == "failed" and "403" in repo.inbox_entry(TENANT, CONSUMER, "bad-1")[1]  # type: ignore[index]
    assert repo.inbox_entry(TENANT, CONSUMER, "good-1") == ("done", None)
    assert METRICS["prediction_consumer_permanent_failures_total"] == before + 2
    # A redelivery of the failed event is a duplicate, not another attempt.
    assert consumer.process([decode(bad)]).duplicates == 1  # type: ignore[list-item]


def test_a_transient_failure_is_retried_and_only_a_transient_one_holds_the_batch():
    fail = Failing({LE}, permanent=False)
    consumer, repo = consumer_with(Spy())
    consumer._recompute = fail  # type: ignore[assignment]
    source = FakeSource([[applied([str(uuid.uuid4())], id_="t-1")]])
    with pytest.raises(RecomputeFailed):
        run_once(source, consumer)
    assert source.commits == 0 and source.rewinds == 1 and repo.inbox_entry(TENANT, CONSUMER, "t-1") is None
    fail.bad = set()
    assert consumer.process([decode(applied([str(uuid.uuid4())], id_="t-1"))]).handled == 1  # type: ignore[list-item]


def test_the_feed_tells_permanent_refusals_from_transient_ones():
    import httpx

    from prediction.receivables import FeedError, ReceivablesFeed
    from prediction.signer import ServiceSigner
    from test_sync import KEY

    def feed(status: int) -> ReceivablesFeed:
        return ReceivablesFeed("http://r", ServiceSigner(KEY), httpx.Client(transport=httpx.MockTransport(lambda _r: httpx.Response(status, json={}))))

    for status, permanent in ((403, True), (404, True), (422, True), (408, False), (429, False), (500, False), (503, False)):
        with pytest.raises(FeedError) as exc:
            list(feed(status).read(TENANT, LE, "open", None))
        assert exc.value.permanent is permanent, status
    unreadable = ReceivablesFeed("http://r", ServiceSigner(KEY), httpx.Client(transport=httpx.MockTransport(lambda _r: httpx.Response(200, json={"nope": 1}))))
    with pytest.raises(FeedError) as exc:
        list(unreadable.read(TENANT, LE, "open", None))
    assert exc.value.permanent is True


def test_delta_reports_which_errors_are_permanent():
    from prediction.sync import delta

    fake, feed, service, _repo = setup()
    fake.fail = 403
    rep = delta(service, feed, TENANT, [LE])
    assert rep.errors and rep.permanent == rep.errors
    fake.fail = 503
    rep = delta(service, feed, TENANT, [LE])
    assert rep.errors and rep.permanent == []
    rep = delta(service, feed, TENANT, ["not-a-uuid"])
    assert rep.permanent == rep.errors


def test_kafka_security_follows_the_cluster_scram_over_tls_and_the_workflow_consumer_names():
    from prediction.consumer import kafka_security

    env = {"KAFKA_USERNAME": "prediction-consumer", "KAFKA_PASSWORD_FILE": "/p", "KAFKA_TLS_CA_FILE": "/ca.crt"}
    assert kafka_security(env.get, lambda path: "s3cret\n") == {
        "security_protocol": "SASL_SSL", "sasl_mechanism": "SCRAM-SHA-512", "sasl_plain_username": "prediction-consumer", "sasl_plain_password": "s3cret", "ssl_cafile": "/ca.crt",
    }
    assert kafka_security({}.get) == {}
    with pytest.raises(ValueError):
        kafka_security({"KAFKA_USERNAME": "u"}.get)
