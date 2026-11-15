"""HTTP surface of the payment prediction service (FR-47.15, PRD 9.47.5).

Identity comes from the edge headers, as for every Passage service: x-passage-tenant,
x-passage-user and x-passage-duties, set by the gateway from the verified token (this service
never decodes a JWT). A missing or malformed tenant or user is 401, a missing duty 403. The
tenant is never taken from the body, so one tenant cannot read or train on another's data.
"""

from __future__ import annotations

import uuid
from datetime import date
from typing import Annotated

from fastapi import Depends, FastAPI, Header, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict, Field

from prediction.domain import MAE_MAX_DAYS_RANGE, MIN_ROWS_RANGE, Config, Model, OpenInvoice, PaidInvoice, Prediction, PredictionError, is_stale
from prediction.logpseudo import install_logging
from prediction.runtime import config_from_env, repository_from_env
from prediction.service import PredictionService

install_logging(os.environ)  # eu-data-boundary overlay or LOG_PSEUDONYMIZE=true: person values and emails in log lines are pseudonymized (#605)

NIL = uuid.UUID(int=0)
DUTY_ADMIN = "agents.admin"  # retrain and score (seeded in policies/authz/data.json)
DUTY_VIEW = "collections.view"  # read a prediction

MAX_PAID = 50_000
MAX_OPEN = 5_000


service = PredictionService(repository_from_env(), config_from_env())
app = FastAPI(title="Passage payment prediction", version="0.1.0")


class Problem(Exception):
    def __init__(self, status: int, code: str, detail: str) -> None:
        super().__init__(detail)
        self.status, self.code, self.detail = status, code, detail


def _problem(status: int, code: str, detail: str, instance: str) -> JSONResponse:
    return JSONResponse(
        {"type": f"https://passage.dev/problems/{code}", "status": status, "code": code, "detail": detail, "instance": instance},
        status_code=status,
        media_type="application/problem+json",
        headers={"cache-control": "no-store"},
    )


@app.exception_handler(Problem)
async def _on_problem(request: Request, exc: Problem) -> JSONResponse:
    return _problem(exc.status, exc.code, exc.detail, request.url.path)


@app.exception_handler(PredictionError)
async def _on_prediction_error(request: Request, exc: PredictionError) -> JSONResponse:
    return _problem(exc.status, exc.code, str(exc), request.url.path)


@app.exception_handler(RequestValidationError)
async def _on_validation(request: Request, exc: RequestValidationError) -> JSONResponse:
    return _problem(400, "PRD-0400", "the request is not valid: " + "; ".join(".".join(map(str, e["loc"][1:])) + " " + e["msg"] for e in exc.errors()[:5]), request.url.path)


ALL_SCOPE_DUTY = "org.switcher.all-scope"  # platform/go/tenant DutyAllScope: a caller who may act in every legal entity


class Caller(BaseModel):
    tenant_id: str
    user_id: str
    duties: frozenset[str]
    legal_entities: frozenset[str] = frozenset()  # X-Passage-Legal-Entity: the entities the caller is scoped to

    def may_see_entity(self, legal_entity_id: str) -> bool:
        """A caller with a legal-entity scope sees those entities only; one with none must hold the all-scope duty."""
        if self.legal_entities:
            return legal_entity_id in self.legal_entities
        return ALL_SCOPE_DUTY in self.duties

    def require(self, duty: str) -> None:
        if duty not in self.duties:
            raise Problem(403, "AGT-0203", f"you do not hold duty {duty}, which this needs")


def _uuid(value: str | None) -> str | None:
    try:
        parsed = uuid.UUID((value or "").strip())
    except ValueError:
        return None
    return None if parsed == NIL else str(parsed)


def caller(
    x_passage_tenant: Annotated[str | None, Header()] = None,
    x_passage_user: Annotated[str | None, Header()] = None,
    x_passage_duties: Annotated[str | None, Header()] = None,
    x_passage_legal_entity: Annotated[str | None, Header()] = None,
) -> Caller:
    tenant, user = _uuid(x_passage_tenant), _uuid(x_passage_user)
    if tenant is None or user is None:
        raise Problem(401, "AGT-0401", "the edge did not identify a tenant and user")
    duties = frozenset(d.strip() for d in (x_passage_duties or "").split(",") if d.strip())
    entities = frozenset(e for e in (_uuid(x) for x in (x_passage_legal_entity or "").split(",")) if e)
    return Caller(tenant_id=tenant, user_id=user, duties=duties, legal_entities=entities)


class InvoiceIn(BaseModel):
    model_config = ConfigDict(extra="forbid")
    invoiceId: str = Field(min_length=1, max_length=64)
    customerId: str = Field(min_length=1, max_length=64)
    amount: float = Field(ge=0, allow_inf_nan=False)
    invoiceDate: date
    dueDate: date
    dunningCount: int = Field(default=0, ge=0, le=20)
    disputes: int = Field(default=0, ge=0, le=20)


class PaidIn(InvoiceIn):
    paidDate: date


class RetrainRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    paidInvoices: list[PaidIn] = Field(max_length=MAX_PAID)


class SettingsRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    minRows: int | None = Field(default=None, ge=MIN_ROWS_RANGE[0], le=MIN_ROWS_RANGE[1])
    maeMaxDays: float | None = Field(default=None, ge=MAE_MAX_DAYS_RANGE[0], le=MAE_MAX_DAYS_RANGE[1], allow_inf_nan=False)


class ScoreRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    openInvoices: list[InvoiceIn] = Field(min_length=1, max_length=MAX_OPEN)


def _open(i: InvoiceIn) -> OpenInvoice:
    if i.dueDate < i.invoiceDate:
        raise Problem(400, "PRD-0400", f"invoice {i.invoiceId}: dueDate is before invoiceDate")
    return OpenInvoice(i.invoiceId, i.customerId, i.amount, i.invoiceDate, i.dueDate, i.dunningCount, i.disputes)


def _model_json(m: Model) -> dict[str, object]:
    return {
        "modelVersion": m.model_version,
        "trainedAt": m.trained_at.isoformat(),
        "rows": m.rows,
        "maeDays": m.mae_days,
        "maeMaxDays": m.mae_max_days,
        "status": m.status,
    }


def _prediction_json(p: Prediction, stale: bool | None = None) -> dict[str, object]:
    out: dict[str, object] = {
        "invoiceId": p.invoice_id,
        "modelVersion": p.model_version,
        "predictedPaymentDate": p.predicted_payment_date.isoformat(),
        "pLate30d": p.p_late_30d,
        "featuresHash": p.features_hash,
        "scoredAt": p.scored_at.isoformat(),
    }
    if stale is not None:
        out["stale"] = stale
    return out


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/readyz")
def readyz() -> JSONResponse:
    ping = getattr(service.repo, "ping", None)
    if ping is not None:
        try:
            ping()
        except Exception:  # noqa: BLE001 - any database failure means not ready
            return JSONResponse({"status": "database unavailable"}, status_code=503)
    return JSONResponse({"status": "ready"})


@app.post("/api/v1/predictions/payment/retrain", include_in_schema=False)
@app.post("/v1/predictions/payment/retrain")
def retrain(body: RetrainRequest, who: Annotated[Caller, Depends(caller)]) -> JSONResponse:
    """Trains on the paid invoices sent (the weekly job sends the tenant's receivables history).
    Fewer than min_rows is 422 AGT-0213. The model is ACTIVE when its held-out MAE is within the
    gate, else CANDIDATE and the previous ACTIVE model keeps serving."""
    who.require(DUTY_ADMIN)
    paid = []
    for i in body.paidInvoices:
        o = _open(i)
        if i.paidDate < i.invoiceDate:
            raise Problem(400, "PRD-0400", f"invoice {i.invoiceId}: paidDate is before invoiceDate")
        paid.append(PaidInvoice(o.invoice_id, o.customer_id, o.amount, o.invoice_date, o.due_date, o.dunning_count, o.disputes, i.paidDate))
    model = service.retrain(who.tenant_id, paid)
    return JSONResponse(_model_json(model), status_code=201)


@app.post("/api/v1/predictions/payment/score", include_in_schema=False)
@app.post("/v1/predictions/payment/score")
def score(body: ScoreRequest, who: Annotated[Caller, Depends(caller)]) -> JSONResponse:
    who.require(DUTY_ADMIN)
    preds = service.score(who.tenant_id, [_open(i) for i in body.openInvoices])
    return JSONResponse({"items": [_prediction_json(p) for p in preds]})


@app.get("/api/v1/ar/invoices/{invoice_id}/prediction", include_in_schema=False)
@app.get("/v1/ar/invoices/{invoice_id}/prediction")
def get_prediction(invoice_id: str, who: Annotated[Caller, Depends(caller)]) -> JSONResponse:
    who.require(DUTY_VIEW)
    found = service.prediction(who.tenant_id, invoice_id)
    if found is None:
        raise Problem(404, "AGT-0404", "no prediction for this invoice")
    prediction, stale = found
    return JSONResponse(_prediction_json(prediction, stale))



def _settings_json(tenant_id: str) -> dict[str, object]:
    cfg = service.config_for(tenant_id)
    return {"minRows": cfg.min_rows, "maeMaxDays": cfg.mae_max_days, "tenantOverride": service.repo.settings(tenant_id) is not None}


@app.get("/api/v1/predictions/payment/settings", include_in_schema=False)
@app.get("/v1/predictions/payment/settings")
def get_settings(who: Annotated[Caller, Depends(caller)]) -> JSONResponse:
    """The tenant's prediction.payment.min_rows and mae_max_days (9.47.20), or the service defaults."""
    who.require(DUTY_ADMIN)
    return JSONResponse(_settings_json(who.tenant_id))


@app.put("/api/v1/predictions/payment/settings", include_in_schema=False)
@app.put("/v1/predictions/payment/settings")
def put_settings(body: SettingsRequest, who: Annotated[Caller, Depends(caller)]) -> JSONResponse:
    """Sets the tenant's parameters; a field left out keeps its current value. The next retrain uses them."""
    who.require(DUTY_ADMIN)
    service.save_settings(who.tenant_id, who.user_id, body.minRows, body.maeMaxDays)
    return JSONResponse(_settings_json(who.tenant_id))


@app.get("/api/v1/predictions/payment/accuracy", include_in_schema=False)
@app.get("/v1/predictions/payment/accuracy")
def get_accuracy(who: Annotated[Caller, Depends(caller)]) -> JSONResponse:
    """The prediction accuracy report: predictions whose invoices have since been paid, MAE, share within 7 days
    and p_late_30d calibration, overall and per model version."""
    who.require(DUTY_ADMIN)
    return JSONResponse(service.accuracy(who.tenant_id))


@app.get("/api/v1/predictions/payment/dso", include_in_schema=False)
@app.get("/v1/predictions/payment/dso")
def get_dso(who: Annotated[Caller, Depends(caller)], legalEntityId: str | None = None) -> JSONResponse:
    """The forecast days to pay (DSO) of a legal entity's open invoices from their stored predictions: the amount-weighted mean of
    predicted payment date less invoice date, the amount-weighted p_late_30d, and how many invoices it covers. 404 AGT-0404 until
    the entity's open invoices have been scored (the receivables feed has run)."""
    who.require(DUTY_VIEW)
    entity = _uuid(legalEntityId)
    if entity is None:
        raise Problem(400, "PRD-0400", "legalEntityId must be a legal entity UUID")
    if not who.may_see_entity(entity):
        # As the Go services answer an entity outside the caller's scope: it is not there for this caller.
        raise Problem(404, "AGT-0404", "no open invoices of this legal entity have been scored yet")
    found = service.repo.dso(who.tenant_id, entity)
    if found is None:
        raise Problem(404, "AGT-0404", "no open invoices of this legal entity have been scored yet")
    return JSONResponse({"legalEntityId": entity, "dsoDays": found.dso_days, "openInvoices": found.open_invoices, "openAmount": found.open_amount,
                         "avgPLate30d": found.avg_p_late_30d, "scoredAt": found.scored_at.isoformat(), "stale": is_stale(found.scored_at, service.clock())})
