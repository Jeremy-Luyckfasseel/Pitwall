"""Round-trip tests proving the generated Pydantic models are a faithful drop-in for
the /contract schemas (Story 3.1, AC1/AC2) — the Python counterpart of
contract/codegen/go/envelope/envelope_test.go and timing/types_test.go.

Every committed *.example.json fixture must survive
model_validate(raw) -> model_dump(mode="json", by_alias=True, exclude_none=False) with
the result structurally equal to the original (dict equality already ignores key
order). exclude_none=False is load-bearing: the wire contract requires a
nullable-but-required field (e.g. causationId) to always serialize as a present key
(null), never be omitted — Pydantic's default excludes nothing, but this is made
explicit here so a future edit that adds exclude_none=True elsewhere doesn't silently
break the contract.
"""

import json
from pathlib import Path

import pytest

from pitwall_contract.envelope_schema import PitwallMessageEnvelope
from pitwall_contract.timing.driver_checked_in_v1_schema import (
    TimingDriverCheckedInV1DataPayload,
)
from pitwall_contract.timing.lap_recorded_v1_schema import (
    TimingLapRecordedV1DataPayload,
)
from pitwall_contract.timing.session_ended_v1_schema import (
    TimingSessionEndedV1DataPayload,
)
from pitwall_contract.timing.session_started_v1_schema import (
    TimingSessionStartedV1DataPayload,
)


def _repo_root() -> Path:
    d = Path(__file__).resolve()
    while d != d.parent:
        if (d / "contract" / "examples").is_dir():
            return d
        d = d.parent
    raise RuntimeError("could not locate repo root (contract/examples)")


def _all_example_fixtures():
    examples_dir = _repo_root() / "contract" / "examples"
    return sorted(examples_dir.rglob("*.example.json"))


@pytest.mark.parametrize("fixture_path", _all_example_fixtures(), ids=lambda p: p.name)
def test_envelope_round_trips_every_committed_example(fixture_path):
    raw = json.loads(fixture_path.read_text(encoding="utf-8"))

    env = PitwallMessageEnvelope.model_validate(raw)
    dumped = env.model_dump(mode="json", by_alias=True, exclude_none=False)

    assert dumped == raw, f"round-trip mismatch for {fixture_path}"


_TIMING_CASES = [
    ("timing/driver.checked_in.v1.example.json", TimingDriverCheckedInV1DataPayload),
    ("timing/lap.recorded.v1.example.json", TimingLapRecordedV1DataPayload),
    ("timing/session.ended.v1.example.json", TimingSessionEndedV1DataPayload),
    ("timing/session.started.v1.example.json", TimingSessionStartedV1DataPayload),
]


@pytest.mark.parametrize("rel_path,model_cls", _TIMING_CASES, ids=[c[0] for c in _TIMING_CASES])
def test_timing_data_payload_round_trips_committed_example(rel_path, model_cls):
    fixture_path = _repo_root() / "contract" / "examples" / rel_path
    raw = json.loads(fixture_path.read_text(encoding="utf-8"))
    data = raw["data"]

    model = model_cls.model_validate(data)
    dumped = model.model_dump(mode="json", by_alias=True, exclude_none=False)

    assert dumped == data, f"round-trip mismatch for {rel_path}"


def test_master_id_rejects_wrong_uuid_version():
    """The masterId pattern pins UUID v4 specifically (version nibble 4, variant
    nibble [89ab]) — proves --type-mappings string+uuid=string didn't silently drop
    the pattern in favor of Python's permissive native UUID type (which would accept
    any UUID version, and brace/urn forms our wire rules explicitly reject)."""
    a_v1_uuid = "018f9e2a-7c3d-1b21-9c4e-2a1b3c4d5e6f"  # version nibble 1, not 4
    payload = {
        "masterId": a_v1_uuid,
        "sessionId": "s1",
        "lapNumber": 1,
        "lapTimeMs": 1000,
        "at": "2026-06-02T14:03:21.512Z",
    }
    with pytest.raises(Exception):
        TimingLapRecordedV1DataPayload.model_validate(payload)
