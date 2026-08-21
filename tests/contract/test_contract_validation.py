"""Exhaustive contract-validation tests for the walking-skeleton timing events.

This is the comprehensive sibling of the lightweight CI gate scripts
(`scripts/validate-contract.py`, `scripts/check-invalid-fixtures.py`,
`scripts/check-schema-lint.py`). The gates prove the *committed* fixtures behave;
this suite proves the *schemas themselves* reject every category of malformed
message that is possible against them — envelope and per-event data — and accept
every well-formed variant.

It validates exactly what production validation enforces: a bare
`Draft202012Validator` with NO format-assertion (JSON-Schema `format` is an
annotation, not an assertion — so `format: uuid` / `format: date-time` do not
reject on their own; the `pattern`s do). If a test asserts a malformed value is
rejected, it is because a `pattern`/`type`/`required`/`minimum` catches it.

Framework: pytest (chosen as the Python-tier standard, AR4). Run: `python -m pytest tests/contract`.
Requires: jsonschema.
"""
import copy
import glob
import json
import os
import subprocess
import sys

import pytest
from jsonschema import Draft202012Validator

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SCHEMAS = os.path.join(ROOT, "contract", "schemas")
EXAMPLES = os.path.join(ROOT, "contract", "examples")


def load(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


ENVELOPE_VALIDATOR = Draft202012Validator(load(os.path.join(SCHEMAS, "envelope.schema.json")))

# event type -> data-payload schema path
EVENT_SCHEMA = {
    "lap.recorded": os.path.join(SCHEMAS, "timing", "lap.recorded.v1.schema.json"),
    "session.started": os.path.join(SCHEMAS, "timing", "session.started.v1.schema.json"),
    "session.ended": os.path.join(SCHEMAS, "timing", "session.ended.v1.schema.json"),
}
DATA_VALIDATOR = {t: Draft202012Validator(load(p)) for t, p in EVENT_SCHEMA.items()}


def errors_for(msg):
    """Full envelope + data validation, exactly as the gate scripts do.
    Returns the list of error messages (empty == the message fully validates)."""
    errs = [f"[envelope] {e.message}" for e in ENVELOPE_VALIDATOR.iter_errors(msg)]
    validator = DATA_VALIDATOR.get(msg.get("type"))
    if validator is not None:
        errs += [f"[data] {e.message}" for e in validator.iter_errors(msg.get("data", {}))]
    return errs


# ---------------------------------------------------------------------------
# Canonical, fully-valid messages (mirror the committed examples).
# ---------------------------------------------------------------------------
def envelope(type_, data):
    return {
        "id": "9f1c2b4e-7a3d-4c8e-bf21-5d0a6e2f1a90",
        "type": type_,
        "source": "timing",
        "schemaVersion": 1,
        "envelopeVersion": 1,
        "occurredAt": "2026-05-31T14:03:21.512Z",
        "correlationId": "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
        "causationId": None,
        "data": data,
    }


LAP_DATA = {
    "masterId": "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
    "sessionId": "session-2026-05-31-evening-heat-3",
    "lapNumber": 7,
    "lapTimeMs": 42318,
    "at": "2026-05-31T14:03:21.500Z",
}
SS_DATA = {"sessionId": "session-x", "startedAt": "2026-05-31T13:45:00.000Z"}
SE_DATA = {"sessionId": "session-x", "endedAt": "2026-05-31T14:05:00.000Z", "summary": []}

VALID = {
    "lap.recorded": lambda: envelope("lap.recorded", copy.deepcopy(LAP_DATA)),
    "session.started": lambda: envelope("session.started", copy.deepcopy(SS_DATA)),
    "session.ended": lambda: envelope("session.ended", copy.deepcopy(SE_DATA)),
}


def with_data(type_, **overrides):
    """Valid message of `type_` with its data shallow-overridden. A value of
    `_DELETE` removes the key."""
    msg = VALID[type_]()
    for k, v in overrides.items():
        if v is _DELETE:
            msg["data"].pop(k, None)
        else:
            msg["data"][k] = v
    return msg


def with_env(type_, **overrides):
    msg = VALID[type_]()
    for k, v in overrides.items():
        if v is _DELETE:
            msg.pop(k, None)
        else:
            msg[k] = v
    return msg


class _Delete:
    pass


_DELETE = _Delete()

UPPER_UUID = "9F1C2B4E-7A3D-4C8E-BF21-5D0A6E2F1A90"
BAD_TS_NO_MILLIS = "2026-05-31T14:03:21Z"
BAD_TS_OFFSET = "2026-05-31T14:03:21.512+00:00"
BAD_TS_DATEONLY = "2026-05-31"


# ===========================================================================
# POSITIVE: every well-formed message validates.
# ===========================================================================
@pytest.mark.parametrize("type_", list(VALID))
def test_canonical_message_validates(type_):
    assert errors_for(VALID[type_]()) == []


@pytest.mark.parametrize("variant", ["8", "9", "a", "b"])
def test_masterid_all_valid_v4_variant_nibbles(variant):
    # v4 variant nibble may be any of 8/9/a/b; all must pass.
    mid = f"1a9f7c20-3e84-4d11-{variant}aa2-7b6c5e4d3f21"
    assert errors_for(with_data("lap.recorded", masterId=mid)) == []


@pytest.mark.parametrize("causation", [None, "018f7b2c-9d31-7a4e-b6c0-1e2f3a4b5c6d"])
def test_causation_id_null_or_lowercase_uuid_valid(causation):
    assert errors_for(with_env("lap.recorded", causationId=causation)) == []


def test_session_ended_summary_may_be_empty_or_populated():
    assert errors_for(with_data("session.ended", summary=[])) == []
    populated = [{"masterId": LAP_DATA["masterId"], "bestLapMs": 41980, "lapCount": 12}]
    assert errors_for(with_data("session.ended", summary=populated)) == []


def test_session_ended_summary_row_requires_only_masterId_and_stays_tolerant():
    # Story 3.3 / Q36.1: masterId is the only REQUIRED summary-item field; bestLapMs
    # and lapCount are optional, and unknown additive fields are still accepted
    # (tolerant reader — no additionalProperties:false on the item).
    assert errors_for(with_data("session.ended", summary=[{"masterId": LAP_DATA["masterId"]}])) == []
    assert errors_for(with_data("session.ended", summary=[{"masterId": LAP_DATA["masterId"], "futureField": "x"}])) == []


def test_tolerant_reader_allows_unknown_additive_field():
    # Additive evolution: an unknown (even snake_case) field that does NOT replace
    # a required one must be accepted (tolerant reader, no additionalProperties:false).
    assert errors_for(with_data("lap.recorded", futureField="x")) == []
    assert errors_for(with_data("lap.recorded", future_field="x")) == []


# ===========================================================================
# NEGATIVE — ENVELOPE: every possible envelope violation is rejected.
# ===========================================================================
ENVELOPE_REQUIRED = [
    "id", "type", "source", "schemaVersion", "envelopeVersion",
    "occurredAt", "correlationId", "causationId", "data",
]


@pytest.mark.parametrize("field", ENVELOPE_REQUIRED)
def test_envelope_missing_required_field_rejected(field):
    assert errors_for(with_env("lap.recorded", **{field: _DELETE})) != []


ENVELOPE_BAD_CASES = {
    "id-uppercase": {"id": UPPER_UUID},
    "id-brace": {"id": "{9f1c2b4e-7a3d-4c8e-bf21-5d0a6e2f1a90}"},
    "id-urn": {"id": "urn:uuid:9f1c2b4e-7a3d-4c8e-bf21-5d0a6e2f1a90"},
    "id-too-short": {"id": "9f1c2b4e"},
    "id-not-uuid": {"id": "definitely-not-a-uuid"},
    "id-wrong-type": {"id": 12345},
    "type-uppercase": {"type": "Lap.Recorded"},
    "type-no-dot": {"type": "laprecorded"},
    "type-leading-digit": {"type": "1lap.recorded"},
    "type-trailing-dot": {"type": "lap.recorded."},
    "type-empty-action": {"type": "lap."},
    "schemaVersion-zero": {"schemaVersion": 0},
    "schemaVersion-negative": {"schemaVersion": -1},
    "schemaVersion-float": {"schemaVersion": 1.5},
    "schemaVersion-string": {"schemaVersion": "1"},
    "envelopeVersion-zero": {"envelopeVersion": 0},
    "envelopeVersion-float": {"envelopeVersion": 1.5},
    "envelopeVersion-string": {"envelopeVersion": "1"},
    "occurredAt-no-millis": {"occurredAt": BAD_TS_NO_MILLIS},
    "occurredAt-offset": {"occurredAt": BAD_TS_OFFSET},
    "occurredAt-date-only": {"occurredAt": BAD_TS_DATEONLY},
    "occurredAt-space-sep": {"occurredAt": "2026-05-31 14:03:21.512Z"},
    "occurredAt-wrong-type": {"occurredAt": 1234567890},
    "correlationId-uppercase": {"correlationId": UPPER_UUID},
    "correlationId-not-uuid": {"correlationId": "nope"},
    "causationId-uppercase": {"causationId": UPPER_UUID},
    "causationId-not-uuid": {"causationId": "nope"},
    "data-string": {"data": "not-an-object"},
    "data-array": {"data": []},
    "data-number": {"data": 5},
}


@pytest.mark.parametrize("case_id", list(ENVELOPE_BAD_CASES))
def test_envelope_violation_rejected(case_id):
    msg = with_env("lap.recorded", **ENVELOPE_BAD_CASES[case_id])
    assert errors_for(msg) != [], f"envelope case {case_id!r} should be rejected"


# ===========================================================================
# NEGATIVE — DATA per event: every possible data violation is rejected.
# Each case is (event_type, {data overrides}).
# ===========================================================================
DATA_BAD_CASES = {
    # ---- lap.recorded ----
    "lap-missing-masterId": ("lap.recorded", {"masterId": _DELETE}),
    "lap-missing-sessionId": ("lap.recorded", {"sessionId": _DELETE}),
    "lap-missing-lapNumber": ("lap.recorded", {"lapNumber": _DELETE}),
    "lap-missing-lapTimeMs": ("lap.recorded", {"lapTimeMs": _DELETE}),
    "lap-missing-at": ("lap.recorded", {"at": _DELETE}),
    "lap-masterId-uppercase": ("lap.recorded", {"masterId": "1A9F7C20-3E84-4D11-9AA2-7B6C5E4D3F21"}),
    "lap-masterId-wrong-version": ("lap.recorded", {"masterId": "1a9f7c20-3e84-1d11-9aa2-7b6c5e4d3f21"}),
    "lap-masterId-wrong-variant": ("lap.recorded", {"masterId": "1a9f7c20-3e84-4d11-1aa2-7b6c5e4d3f21"}),
    "lap-masterId-not-uuid": ("lap.recorded", {"masterId": "not-a-uuid"}),
    "lap-masterId-wrong-type": ("lap.recorded", {"masterId": 123}),
    "lap-sessionId-wrong-type": ("lap.recorded", {"sessionId": 123}),
    "lap-lapNumber-zero": ("lap.recorded", {"lapNumber": 0}),
    "lap-lapNumber-negative": ("lap.recorded", {"lapNumber": -1}),
    "lap-lapNumber-float": ("lap.recorded", {"lapNumber": 1.5}),
    "lap-lapNumber-string": ("lap.recorded", {"lapNumber": "7"}),
    "lap-lapTimeMs-zero": ("lap.recorded", {"lapTimeMs": 0}),
    "lap-lapTimeMs-negative": ("lap.recorded", {"lapTimeMs": -5}),
    "lap-lapTimeMs-float": ("lap.recorded", {"lapTimeMs": 42318.5}),
    "lap-lapTimeMs-string": ("lap.recorded", {"lapTimeMs": "42318"}),
    "lap-at-no-millis": ("lap.recorded", {"at": BAD_TS_NO_MILLIS}),
    "lap-at-offset": ("lap.recorded", {"at": BAD_TS_OFFSET}),
    "lap-at-wrong-type": ("lap.recorded", {"at": 123}),
    "lap-snake-lapTimeMs": ("lap.recorded", {"lapTimeMs": _DELETE, "lap_time_ms": 42318}),
    "lap-snake-sessionId": ("lap.recorded", {"sessionId": _DELETE, "session_id": "s"}),
    "lap-snake-masterId": ("lap.recorded", {"masterId": _DELETE, "master_id": LAP_DATA["masterId"]}),
    "lap-snake-lapNumber": ("lap.recorded", {"lapNumber": _DELETE, "lap_number": 7}),
    # ---- session.started ----
    "ss-missing-sessionId": ("session.started", {"sessionId": _DELETE}),
    "ss-missing-startedAt": ("session.started", {"startedAt": _DELETE}),
    "ss-sessionId-wrong-type": ("session.started", {"sessionId": 123}),
    "ss-startedAt-no-millis": ("session.started", {"startedAt": BAD_TS_NO_MILLIS}),
    "ss-startedAt-offset": ("session.started", {"startedAt": BAD_TS_OFFSET}),
    "ss-startedAt-wrong-type": ("session.started", {"startedAt": 123}),
    "ss-snake-startedAt": ("session.started", {"startedAt": _DELETE, "started_at": SS_DATA["startedAt"]}),
    "ss-snake-sessionId": ("session.started", {"sessionId": _DELETE, "session_id": "s"}),
    # ---- session.ended ----
    "se-missing-sessionId": ("session.ended", {"sessionId": _DELETE}),
    "se-missing-endedAt": ("session.ended", {"endedAt": _DELETE}),
    "se-missing-summary": ("session.ended", {"summary": _DELETE}),
    "se-endedAt-no-millis": ("session.ended", {"endedAt": BAD_TS_NO_MILLIS}),
    "se-endedAt-offset": ("session.ended", {"endedAt": BAD_TS_OFFSET}),
    "se-endedAt-wrong-type": ("session.ended", {"endedAt": 123}),
    "se-summary-object": ("session.ended", {"summary": {}}),
    "se-summary-string": ("session.ended", {"summary": "x"}),
    "se-summary-number": ("session.ended", {"summary": 5}),
    # summary[] item shape pinned by Story 3.3 (Q&A Round 36 / Q36.1): masterId required
    # (same UUID-v4 pattern as everywhere else); bestLapMs/lapCount typed when present.
    "se-summary-row-missing-masterId": ("session.ended", {"summary": [{"bestLapMs": 41980, "lapCount": 12}]}),
    "se-summary-row-masterId-not-uuid": ("session.ended", {"summary": [{"masterId": "not-a-uuid", "bestLapMs": 41980, "lapCount": 12}]}),
    "se-summary-row-masterId-uppercase": ("session.ended", {"summary": [{"masterId": UPPER_UUID, "bestLapMs": 41980, "lapCount": 12}]}),
    "se-summary-row-masterId-wrong-version": ("session.ended", {"summary": [{"masterId": "1a9f7c20-3e84-1d11-9aa2-7b6c5e4d3f21", "bestLapMs": 41980}]}),
    "se-summary-row-masterId-wrong-variant": ("session.ended", {"summary": [{"masterId": "1a9f7c20-3e84-4d11-1aa2-7b6c5e4d3f21", "bestLapMs": 41980}]}),
    "se-summary-row-bestLapMs-zero": ("session.ended", {"summary": [{"masterId": LAP_DATA["masterId"], "bestLapMs": 0}]}),
    "se-summary-row-bestLapMs-float": ("session.ended", {"summary": [{"masterId": LAP_DATA["masterId"], "bestLapMs": 41980.5}]}),
    "se-summary-row-bestLapMs-string": ("session.ended", {"summary": [{"masterId": LAP_DATA["masterId"], "bestLapMs": "41980"}]}),
    "se-summary-row-lapCount-negative": ("session.ended", {"summary": [{"masterId": LAP_DATA["masterId"], "lapCount": -1}]}),
    "se-summary-row-lapCount-float": ("session.ended", {"summary": [{"masterId": LAP_DATA["masterId"], "lapCount": 12.5}]}),
    "se-snake-endedAt": ("session.ended", {"endedAt": _DELETE, "ended_at": SE_DATA["endedAt"]}),
    "se-snake-sessionId": ("session.ended", {"sessionId": _DELETE, "session_id": "s"}),
}


@pytest.mark.parametrize("case_id", list(DATA_BAD_CASES))
def test_data_violation_rejected(case_id):
    type_, overrides = DATA_BAD_CASES[case_id]
    msg = with_data(type_, **overrides)
    assert errors_for(msg) != [], f"data case {case_id!r} should be rejected"


# ===========================================================================
# Committed fixtures: examples validate, *.invalid.json are rejected, pairs exist.
# ===========================================================================
def _rel(path, base):
    return os.path.relpath(path, base).replace(os.sep, "/")


EXAMPLE_FILES = sorted(glob.glob(os.path.join(EXAMPLES, "**", "*.example.json"), recursive=True))
INVALID_FILES = sorted(glob.glob(os.path.join(EXAMPLES, "**", "*.invalid.json"), recursive=True))


@pytest.mark.parametrize("path", EXAMPLE_FILES, ids=[_rel(p, EXAMPLES) for p in EXAMPLE_FILES])
def test_committed_example_validates(path):
    msg = load(path)
    # only assert data-schema coverage for events this suite knows; envelope always applies
    errs = [f"[envelope] {e.message}" for e in ENVELOPE_VALIDATOR.iter_errors(msg)]
    schema_path = os.path.join(SCHEMAS, _rel(path, EXAMPLES).replace(".example.json", ".schema.json"))
    if os.path.exists(schema_path):
        errs += [f"[data] {e.message}" for e in Draft202012Validator(load(schema_path)).iter_errors(msg.get("data", {}))]
    assert errs == [], f"{_rel(path, EXAMPLES)} should validate: {errs}"


@pytest.mark.parametrize("path", INVALID_FILES, ids=[_rel(p, EXAMPLES) for p in INVALID_FILES])
def test_committed_invalid_fixture_is_rejected(path):
    msg = load(path)
    errs = [f"[envelope] {e.message}" for e in ENVELOPE_VALIDATOR.iter_errors(msg)]
    schema_path = os.path.join(SCHEMAS, _rel(path, EXAMPLES).replace(".invalid.json", ".schema.json"))
    if os.path.exists(schema_path):
        errs += [f"[data] {e.message}" for e in Draft202012Validator(load(schema_path)).iter_errors(msg.get("data", {}))]
    assert errs != [], f"{_rel(path, EXAMPLES)} is a known-bad fixture but VALIDATED"


@pytest.mark.parametrize("event", ["lap.recorded", "session.started", "session.ended"])
def test_timing_event_has_paired_example_and_invalid(event):
    base = os.path.join(EXAMPLES, "timing", f"{event}.v1")
    assert os.path.exists(base + ".example.json"), f"{event} missing example"
    assert os.path.exists(base + ".invalid.json"), f"{event} missing invalid fixture"


def test_every_invalid_fixture_documents_a_reason():
    for path in INVALID_FILES:
        assert "_invalidReason" in load(path), f"{_rel(path, EXAMPLES)} lacks an _invalidReason"


# ===========================================================================
# The gate scripts and their selftests pass (defence in depth across the layer).
# ===========================================================================
@pytest.mark.parametrize("argv", [
    ["scripts/validate-contract.py"],
    ["scripts/check-schema-lint.py"],
    ["scripts/check-schema-lint.py", "--selftest"],
    ["scripts/check-invalid-fixtures.py"],
    ["scripts/check-invalid-fixtures.py", "--selftest"],
])
def test_gate_script_exits_zero(argv):
    result = subprocess.run(
        [sys.executable, *argv],
        cwd=ROOT,
        stdin=subprocess.DEVNULL,  # avoid invalid-handle inheritance under pytest capture (Windows)
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, f"{' '.join(argv)} exited {result.returncode}\n{result.stdout}\n{result.stderr}"
