"""Log pseudonymization (#605, EU Data Boundary): no raw email, name or client address reaches the written line, pseudonyms are tenant-keyed."""

from __future__ import annotations

import base64
import io
import json
import logging
import unittest

from prediction import logpseudo as lp

TENANT_A = "11111111-1111-4111-8111-111111111111"
TENANT_B = "22222222-2222-4222-8222-222222222222"
# Master key bytes 0..31, the key platform/go/logpseudo's TestKnownAnswers uses.
KEY_B64 = base64.b64encode(bytes(range(32))).decode()
ENV_KEYS = {"FIELDCRYPT_KEYS": f"k1={KEY_B64}", "FIELDCRYPT_CURRENT_KEY_ID": "k1"}
KEYS = lp.MacKeys.from_env(ENV_KEYS)


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        skip = set(logging.makeLogRecord({}).__dict__) | {"message", "asctime"}
        line = {"message": record.getMessage(), **{k: v for k, v in record.__dict__.items() if k not in skip}}
        if record.exc_text:
            line["exception"] = record.exc_text
        return json.dumps(line, default=str)


def logger_with(name: str, filter_: logging.Filter | None) -> tuple[logging.Logger, io.StringIO]:
    buf = io.StringIO()
    handler = logging.StreamHandler(buf)
    handler.setFormatter(JsonFormatter())
    if filter_ is not None:
        handler.addFilter(filter_)
    log = logging.getLogger(name)
    log.handlers, log.propagate, log.filters = [handler], False, []
    log.setLevel(logging.INFO)
    return log, buf


class TestPseudonyms(unittest.TestCase):
    def test_match_the_go_implementation_byte_for_byte(self):
        self.assertEqual(lp.pseudonym(KEYS, TENANT_A, "Jane.Doe@Example.com"), "pn_81c1af9dc7db4ac7")
        self.assertEqual(lp.pseudonym(KEYS, TENANT_B, "Jane.Doe@Example.com"), "pn_cc6b2b4cc9040118")

    def test_none_without_a_key_or_tenant(self):
        self.assertIsNone(lp.pseudonym(None, TENANT_A, "x"))
        self.assertIsNone(lp.pseudonym(KEYS, "", "x"))


class TestRecords(unittest.TestCase):
    def test_no_raw_email_or_name_survives_and_unrelated_fields_pass(self):
        out = lp.pseudonymize_record(
            {"message": "sign-in of jane.doe@example.com", "tenant_id": TENANT_A, "user": "Jane Doe", "email": "jane.doe@example.com",
             "remoteIp": "203.0.113.9", "req": {"subject": "auth0|jane", "tags": ["jane.doe@example.com"]}, "run_id": "r-1", "ok": True},
            KEYS,
        )
        text = json.dumps(out)
        for raw in ("jane.doe", "Jane", "example.com", "203.0.113.9", "auth0"):
            self.assertNotIn(raw, text)
        self.assertEqual((out["run_id"], out["ok"], out["tenant_id"]), ("r-1", True, TENANT_A))
        self.assertEqual(out["email"], lp.pseudonym(KEYS, TENANT_A, "jane.doe@example.com"))

    def test_no_key_or_no_tenant_gives_markers_never_the_value(self):
        no_key = json.dumps(lp.pseudonymize_record({"tenant_id": TENANT_A, "email": "a@example.com", "note": "cc b@example.com"}, None))
        no_tenant = json.dumps(lp.pseudonymize_record({"email": "a@example.com", "note": "cc b@example.com"}, KEYS))
        for out in (no_key, no_tenant):
            self.assertNotIn("example.com", out)
            self.assertIn(lp.REDACTED, out)
            self.assertIn(lp.EMAIL_REDACTED, out)


class TestFilter(unittest.TestCase):
    def test_what_the_handler_writes_has_no_raw_email_or_name(self):
        log, buf = logger_with("test.prediction.filter", lp.PseudonymizingFilter(KEYS))
        log.info("sign-in of %s failed", "jane.doe@example.com", extra={"tenant_id": TENANT_A, "user": "Jane Doe", "run_id": "r-1"})
        out = buf.getvalue()
        for raw in ("jane.doe", "Jane", "example.com"):
            self.assertNotIn(raw, out)
        self.assertEqual(json.loads(out)["run_id"], "r-1")


class TestInstallLogging(unittest.TestCase):
    def tearDown(self):
        for name in ("uvicorn", "uvicorn.error", "uvicorn.access"):
            logging.getLogger(name).filters = []

    def test_on_under_the_eu_overlay_and_with_the_flag_only(self):
        for env, want in (
            ({}, False), ({"LOG_PSEUDONYMIZE": "false"}, False), ({"LOG_PSEUDONYMIZE": "true"}, True),
            ({"PASSAGE_CLOUD_PROFILE": "gcc"}, False), ({"PASSAGE_CLOUD_PROFILE": "commercial+eu-data-boundary"}, True),
        ):
            self.assertEqual(lp.install_logging(env), want, env)
            self.tearDown()

    def test_the_access_log_client_address_and_emails_in_paths_are_scrubbed(self):
        self.assertTrue(lp.install_logging({"LOG_PSEUDONYMIZE": "true", **ENV_KEYS}))
        access, buf = logger_with("uvicorn.access", None)
        access.addFilter(lp._AccessLogFilter(KEYS))
        # uvicorn's access record: client address, method, path, HTTP version, status.
        access.info('%s - "%s %s HTTP/%s" %d', "203.0.113.9:51234", "GET", "/v1/users/jane.doe@example.com", "1.1", 200)
        out = buf.getvalue()
        self.assertNotIn("203.0.113.9", out)
        self.assertNotIn("jane.doe", out)
        self.assertIn("GET", out)

    def test_it_filters_uvicorns_own_loggers(self):
        self.assertTrue(lp.install_logging({"LOG_PSEUDONYMIZE": "true", **ENV_KEYS}))
        for name in ("uvicorn", "uvicorn.error", "uvicorn.access"):
            self.assertTrue(any(isinstance(f, lp.PseudonymizingFilter) for f in logging.getLogger(name).filters), name)
        # A second call does not stack filters.
        lp.install_logging({"LOG_PSEUDONYMIZE": "true", **ENV_KEYS})
        self.assertEqual(len(logging.getLogger("uvicorn").filters), 1)

    def test_a_bad_key_raises_instead_of_leaving_logs_raw(self):
        with self.assertRaises(ValueError):
            lp.install_logging({"LOG_PSEUDONYMIZE": "true", "FIELDCRYPT_KEYS": "k1=bad", "FIELDCRYPT_CURRENT_KEY_ID": "k1"})


if __name__ == "__main__":
    unittest.main()
