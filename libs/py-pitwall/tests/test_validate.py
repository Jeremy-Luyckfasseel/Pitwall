import json
from pathlib import Path

import pytest

from pitwall.validate import Validator, resolve_contract_dir


def _repo_root() -> Path:
    d = Path(__file__).resolve()
    while d != d.parent:
        if (d / "contract" / "schemas" / "envelope.schema.json").is_file():
            return d
        d = d.parent
    raise RuntimeError("could not locate repo root")


@pytest.fixture(scope="module")
def contract_dir() -> str:
    return str(_repo_root() / "contract")


@pytest.fixture(scope="module")
def validator(contract_dir: str) -> Validator:
    return Validator(contract_dir)


def test_resolve_contract_dir_honors_explicit(tmp_path):
    explicit = str(tmp_path)
    assert resolve_contract_dir(explicit) == explicit


def test_resolve_contract_dir_walks_up_from_cwd(contract_dir, monkeypatch):
    monkeypatch.chdir(Path(contract_dir).parent / "libs" / "py-pitwall")
    assert resolve_contract_dir(None) == contract_dir


def test_valid_lap_recorded_example_passes(validator: Validator, contract_dir: str):
    fixture = Path(contract_dir) / "examples" / "timing" / "lap.recorded.v1.example.json"
    raw = fixture.read_bytes()
    # Must not raise.
    validator.validate_envelope_bytes(raw)


def test_known_bad_fixture_is_rejected(validator: Validator, contract_dir: str):
    fixture = Path(contract_dir) / "examples" / "timing" / "session.started.v1.invalid.json"
    raw = fixture.read_bytes()
    with pytest.raises(Exception):
        validator.validate_envelope_bytes(raw)


def test_unregistered_type_fails_closed(validator: Validator):
    payload = json.dumps(
        {
            "id": "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f",
            "type": "no.such.event",
            "source": "driver",
            "schemaVersion": 1,
            "envelopeVersion": 1,
            "occurredAt": "2026-06-02T14:03:21.512Z",
            "correlationId": "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
            "causationId": None,
            "data": {},
        }
    ).encode("utf-8")
    with pytest.raises(Exception):
        validator.validate_envelope_bytes(payload)


def test_validate_heartbeat(validator: Validator, contract_dir: str):
    fixture = Path(contract_dir) / "examples" / "control" / "control.heartbeat.v1.example.json"
    raw = json.loads(fixture.read_text(encoding="utf-8"))
    # Must not raise.
    validator.validate_heartbeat(raw)


def test_every_committed_valid_example_passes(validator: Validator, contract_dir: str):
    examples_dir = Path(contract_dir) / "examples"
    fixtures = sorted(examples_dir.rglob("*.example.json"))
    assert len(fixtures) > 0
    for fixture in fixtures:
        validator.validate_envelope_bytes(fixture.read_bytes())


def test_every_committed_invalid_fixture_is_rejected(validator: Validator, contract_dir: str):
    examples_dir = Path(contract_dir) / "examples"
    fixtures = sorted(examples_dir.rglob("*.invalid.json"))
    assert len(fixtures) > 0
    for fixture in fixtures:
        with pytest.raises(Exception):
            validator.validate_envelope_bytes(fixture.read_bytes())
