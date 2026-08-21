"""Pure, I/O-free tests for driver.domain.history (Story 3.3). Mirrors
test_domain_profile.py's style: no DB, no broker. Proves build_history_appended
produces a CAUSED envelope that round-trips through the REAL /contract validator.
"""

import json
from pathlib import Path

import pytest
from driver.domain.history import HISTORY_APPENDED_ROUTING_KEY, build_history_appended
from pitwall.validate import Validator, resolve_contract_dir

MASTER_ID = "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"


def _repo_root() -> Path:
    d = Path(__file__).resolve()
    while d != d.parent:
        if (d / "contract" / "schemas" / "envelope.schema.json").is_file():
            return d
        d = d.parent
    raise RuntimeError("could not locate repo root")


@pytest.fixture(scope="module")
def validator() -> Validator:
    return Validator(resolve_contract_dir(str(_repo_root() / "contract")))


def test_build_history_appended_is_caused_by_the_triggering_envelope():
    correlation_id = "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55"
    causation_id = "aa11bb22-cc33-4dd4-8ee5-ff6677889900"

    env = build_history_appended(
        source="driver",
        correlation_id=correlation_id,
        causation_id=causation_id,
        occurred_at="2026-06-02T14:03:21.512Z",
        master_id=MASTER_ID,
        session_id="session-x",
        best_lap_ms=41980,
        lap_count=12,
    )

    assert env.type == HISTORY_APPENDED_ROUTING_KEY
    assert env.source == "driver"
    assert env.correlation_id == correlation_id
    assert env.causation_id == causation_id  # caused by the session.ended, never null

    dumped = env.model_dump(mode="json", by_alias=True, exclude_none=False)
    assert dumped["data"] == {
        "masterId": MASTER_ID,
        "sessionId": "session-x",
        "bestLapMs": 41980,
        "lapCount": 12,
    }


def test_build_history_appended_carries_null_stats_when_absent():
    # A summary row may carry no bestLapMs/lapCount (driver logged no counted lap);
    # the keys are still present on the wire (no omitempty), serialized as null.
    env = build_history_appended(
        source="driver",
        correlation_id="8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
        causation_id="aa11bb22-cc33-4dd4-8ee5-ff6677889900",
        occurred_at="2026-06-02T14:03:21.512Z",
        master_id=MASTER_ID,
        session_id="session-x",
        best_lap_ms=None,
        lap_count=None,
    )
    dumped = env.model_dump(mode="json", by_alias=True, exclude_none=False)
    assert dumped["data"] == {
        "masterId": MASTER_ID,
        "sessionId": "session-x",
        "bestLapMs": None,
        "lapCount": None,
    }


def test_build_history_appended_round_trips_through_the_real_contract_validator(validator: Validator):
    """TDD-red-shown proof: an honestly-built envelope validates against the REAL
    committed /contract schema; a post-construction corruption (bypassing the DTO's
    own constructor) makes the SAME validator call raise, then reverts to green."""
    env = build_history_appended(
        source="driver",
        correlation_id="8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
        causation_id="aa11bb22-cc33-4dd4-8ee5-ff6677889900",
        occurred_at="2026-06-02T14:03:21.512Z",
        master_id=MASTER_ID,
        session_id="session-x",
        best_lap_ms=41980,
        lap_count=12,
    )
    good = json.loads(env.model_dump_json(by_alias=True, exclude_none=False))
    validator.validate_envelope_bytes(json.dumps(good).encode("utf-8"))

    bad = json.loads(json.dumps(good))
    bad["data"]["masterId"] = "1a9f7c20-3e84-1d11-9aa2-7b6c5e4d3f21"  # v1 UUID
    with pytest.raises(Exception):
        validator.validate_envelope_bytes(json.dumps(bad).encode("utf-8"))

    validator.validate_envelope_bytes(json.dumps(good).encode("utf-8"))  # reverted, green again
