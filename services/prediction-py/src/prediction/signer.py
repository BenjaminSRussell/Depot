"""Service tokens for the calls prediction makes to other services (platform/go/tenant/service.go, the same wire format as the
verifier in dispatch-py): a JSON payload and an Ed25519 signature over `depot.service-token.v1.` and the encoded payload."""

from __future__ import annotations

import base64
import json
import time
import uuid
from collections.abc import Callable, Mapping
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

HEADER = "X-Depot-Service-Token"
SERVICE_NAME = "prediction"
ACTOR = "service:prediction-sync"
TOKEN_CONTEXT = b"depot.service-token.v1."
TTL_SECONDS = 60

# What this service may ask of whom; the grant table of platform/go/tenant/service.go and policies/authz/data.json, service_grants.
GRANTS: Mapping[str, tuple[str, ...]] = {"receivables": ("receivables.prediction.read",)}


class SignerError(Exception):
    pass


def _b64(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


class ServiceSigner:
    def __init__(self, key: Ed25519PrivateKey, name: str = SERVICE_NAME, kid: str = "", now: Callable[[], float] = time.time) -> None:
        self._key, self.name, self._kid, self._now = key, name, kid, now

    @classmethod
    def from_env(cls, getenv: Callable[[str], str | None]) -> ServiceSigner | None:
        """None when no signing key is configured (the feed is then off). DEPOT_SERVICE_SIGNING_KEY is the base64 32-byte seed, or
        DEPOT_SERVICE_SIGNING_KEY_FILE names a file holding it; the service name must be `prediction` for the grant to apply."""
        raw = (getenv("DEPOT_SERVICE_SIGNING_KEY") or "").strip()
        path = getenv("DEPOT_SERVICE_SIGNING_KEY_FILE")
        if path:
            raw = Path(path).read_text(encoding="utf-8").strip()
            if not raw:
                raise SignerError("DEPOT_SERVICE_SIGNING_KEY_FILE names an empty file")
        if not raw:
            return None
        name = getenv("DEPOT_SERVICE_NAME") or SERVICE_NAME
        if name != SERVICE_NAME:
            raise SignerError(f"DEPOT_SERVICE_NAME must be {SERVICE_NAME!r} for the service grants, got {name!r}")
        try:
            seed = base64.b64decode(raw, validate=True)
            key = Ed25519PrivateKey.from_private_bytes(seed)
        except ValueError as exc:
            raise SignerError("the signing key must be a base64 32-byte Ed25519 seed") from exc
        return cls(key, name, getenv("DEPOT_SERVICE_KEY_ID") or "")

    def mint(self, aud: str, tenant_id: str, legal_entity_id: str, duties: list[str]) -> str:
        """A token for one call: audience, tenant, one legal entity and only duties of this service's grant on that audience."""
        granted = GRANTS.get(aud, ())
        if not duties or any(d not in granted for d in duties):
            raise SignerError(f"{self.name} holds no grant for {duties} on {aud}")
        for value in (tenant_id, legal_entity_id):
            if str(uuid.UUID(value)) != value or value == str(uuid.UUID(int=0)):
                raise SignerError("a service token needs a tenant and one legal entity UUID")
        now = int(self._now())
        claims = {"iss": self.name, "aud": aud, "tid": tenant_id, "le": legal_entity_id, "act": ACTOR, "duties": duties,
                  "iat": now, "exp": now + TTL_SECONDS, "jti": uuid.uuid4().hex}
        if self._kid:
            claims["kid"] = self._kid
        body = _b64(json.dumps(claims, separators=(",", ":")).encode())
        return body + "." + _b64(self._key.sign(TOKEN_CONTEXT + body.encode()))
