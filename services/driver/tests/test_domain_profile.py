"""Pure, I/O-free tests for driver.domain.profile (Story 3.2). Mirrors
services/identity/internal/domain/identity_test.go's style: no DB, no broker.
"""

import json
from pathlib import Path

import pytest
from driver.domain.profile import PROFILE_UPDATED_ROUTING_KEY, MinimalProfile, build_profile_updated
from pitwall.validate import Validator, resolve_contract_dir


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


def test_minimal_profile_defaults_to_all_unset():
    profile = MinimalProfile(master_id="1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")
    assert profile.racing_number is None
    assert profile.kart_class is None
    assert profile.nickname is None


def test_build_profile_updated_is_caused_by_the_triggering_envelope():
    correlation_id = "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55"
    causation_id = "aa11bb22-cc33-4dd4-8ee5-ff6677889900"
    profile = MinimalProfile(master_id="1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")

    env = build_profile_updated(
        source="driver",
        correlation_id=correlation_id,
        causation_id=causation_id,
        occurred_at="2026-06-02T14:03:21.512Z",
        profile=profile,
    )

    assert env.type == PROFILE_UPDATED_ROUTING_KEY
    assert env.source == "driver"
    assert env.correlation_id == correlation_id
    assert env.causation_id == causation_id  # caused, never flow-originating (never null)

    dumped = env.model_dump(mode="json", by_alias=True, exclude_none=False)
    assert dumped["data"] == {
        "masterId": "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
        "racingNumber": None,
        "kartClass": None,
        "nickname": None,
    }


def test_build_profile_updated_carries_set_fields():
    profile = MinimalProfile(
        master_id="1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
        racing_number="27",
        kart_class="senior",
        nickname="Sofie-S",
    )

    env = build_profile_updated(
        source="driver",
        correlation_id="8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
        causation_id="aa11bb22-cc33-4dd4-8ee5-ff6677889900",
        occurred_at="2026-06-02T14:03:21.512Z",
        profile=profile,
    )

    dumped = env.model_dump(mode="json", by_alias=True, exclude_none=False)
    assert dumped["data"]["racingNumber"] == "27"
    assert dumped["data"]["kartClass"] == "senior"
    assert dumped["data"]["nickname"] == "Sofie-S"


def test_build_profile_updated_round_trips_through_the_real_contract_validator(validator: Validator):
    """TDD-red-shown proof (Story 3.1's own discipline): a built envelope must
    validate against the REAL committed /contract schema, not just an in-memory
    Pydantic model. The envelope is corrupted AFTER construction (mutating the
    dumped JSON dict directly, bypassing the generated DTO's own constructor-time
    pattern check) so this test isolates the /contract Validator's own enforcement
    rather than re-testing Pydantic's constr validation."""
    profile = MinimalProfile(master_id="1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", nickname="Sofie-S")
    env = build_profile_updated(
        source="driver",
        correlation_id="8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
        causation_id="aa11bb22-cc33-4dd4-8ee5-ff6677889900",
        occurred_at="2026-06-02T14:03:21.512Z",
        profile=profile,
    )
    good = json.loads(env.model_dump_json(by_alias=True, exclude_none=False))

    # Green: the honestly-built envelope validates.
    validator.validate_envelope_bytes(json.dumps(good).encode("utf-8"))

    # Red (shown here rather than as a separate run): corrupt masterId to a v1 UUID
    # (bypassing the DTO's constructor so ONLY the /contract Validator is exercised)
    # and confirm the SAME validator call now raises; then revert and re-confirm green.
    bad = json.loads(json.dumps(good))
    bad["data"]["masterId"] = "1a9f7c20-3e84-1d11-9aa2-7b6c5e4d3f21"
    with pytest.raises(Exception):
        validator.validate_envelope_bytes(json.dumps(bad).encode("utf-8"))

    validator.validate_envelope_bytes(json.dumps(good).encode("utf-8"))  # reverted, green again
