import os

from fastapi import FastAPI
from pydantic import BaseModel, Field

from configurator.logpseudo import install_logging
from configurator.model import load_fixture
from configurator.solve import solve

install_logging(os.environ)  # eu-data-boundary overlay or LOG_PSEUDONYMIZE=true: person values and emails in log lines are pseudonymized (#605)
app = FastAPI(title="depot-configurator", version="0.1.0")
FIXTURE = load_fixture()


class SolveRequest(BaseModel):
    selections: dict[str, str] = Field(default_factory=dict)


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/readyz")
def readyz() -> dict[str, str]:
    return {"status": "ready"}


@app.post("/v1/configurations:solve")
def solve_endpoint(req: SolveRequest) -> dict:
    return solve(FIXTURE, req.selections)


@app.post("/v1/configurations:validate")
def validate_endpoint(req: SolveRequest) -> dict:
    return solve(FIXTURE, req.selections)
