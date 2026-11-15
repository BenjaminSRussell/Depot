"""Client of the receivables invoice feed (GET /v1/receivables/prediction-invoices, duty receivables.prediction.read)."""

from __future__ import annotations

from collections.abc import Callable, Iterator
from dataclasses import dataclass
from datetime import date, datetime
from typing import Any

import httpx

from prediction.domain import OpenInvoice, PaidInvoice
from prediction.signer import HEADER, ServiceSigner

DUTY = "receivables.prediction.read"
PATH = "/v1/receivables/prediction-invoices"
PAGE = 500
MAX_PAGES = 2000


class FeedError(Exception):
    """The feed could not be read completely; the caller keeps its watermark and tries again."""


@dataclass(frozen=True)
class Page:
    invoices: list[OpenInvoice]
    updated_at: datetime | None


def _day(value: str) -> date:
    return date.fromisoformat(value)


def _invoice(row: dict[str, Any], paid: bool) -> OpenInvoice:
    base = (row["id"], row["customerId"], float(row["amount"]), _day(row["documentDate"]), _day(row["dueDate"]), int(row.get("dunningLevel", 0)), int(row.get("disputes", 0)))
    if paid:
        return PaidInvoice(*base, paid_date=_day(row["paidDate"]))
    return OpenInvoice(*base)


class ReceivablesFeed:
    def __init__(self, base_url: str, signer: ServiceSigner, client: httpx.Client | None = None) -> None:
        self._base, self._signer = base_url.rstrip("/"), signer
        self._client = client or httpx.Client(timeout=30.0)

    def read(self, tenant_id: str, legal_entity_id: str, state: str, since: datetime | None) -> Iterator[Page]:
        """Pages of the entity's invoices in `state` (open or paid) changed at or after `since`, in change order. A refusal, an
        unreadable answer or an endless listing is a FeedError, so a partial read never passes for the whole feed."""
        cursor = ""
        for _ in range(MAX_PAGES):
            params = {"state": state, "limit": str(PAGE)}
            if since is not None:
                params["updated_since"] = since.isoformat()
            if cursor:
                params["cursor"] = cursor
            token = self._signer.mint("receivables", tenant_id, legal_entity_id, [DUTY])
            try:
                res = self._client.get(self._base + PATH, params=params, headers={HEADER: token})
            except httpx.HTTPError as exc:
                raise FeedError(f"receivables is not reachable: {exc}") from exc
            if res.status_code != 200:
                raise FeedError(f"receivables answered {res.status_code} for the {state} invoice feed")
            try:
                body = res.json()
                rows = body["items"]
                invoices = [_invoice(r, state == "paid") for r in rows]
                last = datetime.fromisoformat(rows[-1]["updatedAt"]) if rows else None
            except (ValueError, KeyError, TypeError) as exc:
                raise FeedError(f"the {state} invoice feed is not readable: {exc}") from exc
            yield Page(invoices, last)
            cursor = body.get("nextCursor") or ""
            if not cursor:
                return
        raise FeedError(f"receivables lists more than {MAX_PAGES * PAGE} {state} invoices; the feed is not read completely")
