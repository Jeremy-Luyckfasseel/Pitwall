"""Pure, I/O-free tests for driver.domain.pr (Story 3.4). Mirrors test_domain_history's
style: no DB, no broker. Proves build_pr_updated produces a CAUSED envelope that
round-trips through the REAL /contract validator.
"""

import json
from pathlib import Path

import pytest
from driver.domain.pr import PR_UPDATED_ROUTING_KEY, build_pr_updated
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


def test_build_pr_updated_is_caused_by_the_triggering_break():
    correlation_id = "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55"
    causation_id = "aa11bb22-cc33-4dd4-8ee5-ff6677889900"

    env = build_pr_updated(
        source="driver",
        correlation_id=correlation_id,
        causation_id=causation_id,
        occurred_at="2026-06-02T14:03:21.512Z",
        master_id=MASTER_ID,
        lap_time_ms=41980,
        set_at="2026-05-31T14:02:00.000Z",
    )

    assert env.type == PR_UPDATED_ROUTING_KEY
    assert env.source == "driver"
    assert env.correlation_id == correlation_id
    assert env.causation_id == causation_id  # caused by personal_record.broken, never null

    dumped = env.model_dump(mode="json", by_alias=True, exclude_none=False)
    assert dumped["data"] == {
        "masterId": MASTER_ID,
        "lapTimeMs": 41980,
        "setAt": "2026-05-31T14:02:00.000Z",
    }


def test_build_pr_updated_round_trips_through_the_real_contract_validator(validator: Validator):
    env = build_pr_updated(
        source="driver",
        correlation_id="8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
        causation_id="aa11bb22-cc33-4dd4-8ee5-ff6677889900",
        occurred_at="2026-06-02T14:03:21.512Z",
        master_id=MASTER_ID,
        lap_time_ms=41980,
        set_at="2026-05-31T14:02:00.000Z",
    )
    good = json.loads(env.model_dump_json(by_alias=True, exclude_none=False))
    validator.validate_envelope_bytes(json.dumps(good).encode("utf-8"))

    bad = json.loads(json.dumps(good))
    bad["data"]["setAt"] = "2026-05-31T14:02:00Z"  # missing millis -> pattern rejects
    with pytest.raises(Exception):
        validator.validate_envelope_bytes(json.dumps(bad).encode("utf-8"))

    validator.validate_envelope_bytes(json.dumps(good).encode("utf-8"))  # reverted, green again
