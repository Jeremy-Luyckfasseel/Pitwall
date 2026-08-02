"""The /contract JSON-Schema validator wrapper (blueprint §Messaging: validate every
message in and out). Compiles the envelope schema, the heartbeat data schema, and an
index of every event data schema keyed by its wire `type`, once at construction. A
message that fails is logged + quarantined/rejected by the CALLER, never published nor
applied (CLAUDE.md rule 5) — this module only raises; callers decide the sad-path.

Deliberately independent of the /contract-generated Pydantic models (contract/codegen/
python) — codegen and validation are separate concerns here exactly as in the Go
library (architecture §Schema-as-Source & Codegen: "the validator is a thin
per-language wrapper over a standard JSON-Schema library").
"""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any

import jsonschema
from jsonschema.validators import Draft202012Validator

_VERSION_SUFFIX = re.compile(r"\.v\d+$")


def resolve_contract_dir(explicit: str | None) -> str:
    """Returns the directory holding the /contract tree. Honors an explicit path (set
    in the container, where /contract is COPYed in) and otherwise walks up from the
    working directory to find it (dev/test) — mirrors Go's ResolveContractDir."""
    if explicit:
        return explicit
    d = Path.cwd()
    while True:
        candidate = d / "contract" / "schemas" / "envelope.schema.json"
        if candidate.is_file():
            return str(d / "contract")
        if d.parent == d:
            raise RuntimeError("could not locate the /contract directory (set CONTRACT_DIR)")
        d = d.parent


class ContractValidationError(Exception):
    """Raised when a message fails /contract validation (envelope or data shape) or
    names a `type` with no registered data schema (fail closed — a service must never
    publish nor apply an event it cannot validate)."""


def _compile(path: Path) -> Draft202012Validator:
    with path.open("r", encoding="utf-8") as f:
        schema = json.load(f)
    validator_cls = jsonschema.validators.validator_for(schema, default=Draft202012Validator)
    validator_cls.check_schema(schema)
    return validator_cls(schema)


def _index_data_schemas(schemas_dir: Path) -> dict[str, Draft202012Validator]:
    out: dict[str, Draft202012Validator] = {}
    for path in schemas_dir.rglob("*.schema.json"):
        if path.name == "envelope.schema.json":
            continue
        stem = path.name.removesuffix(".schema.json")
        wire_type = _VERSION_SUFFIX.sub("", stem)
        out[wire_type] = _compile(path)
    return out


class Validator:
    """Validates messages against the /contract JSON Schemas."""

    def __init__(self, contract_dir: str):
        schemas_dir = Path(contract_dir) / "schemas"
        self._envelope = _compile(schemas_dir / "envelope.schema.json")
        self._heartbeat = _compile(schemas_dir / "control" / "control.heartbeat.v1.schema.json")
        self._data = _index_data_schemas(schemas_dir)

    def validate_envelope_bytes(self, payload: bytes) -> None:
        """Validates a marshalled message: the full envelope against the envelope
        schema, then its data against the schema registered for the envelope's `type`.
        Fails closed on an unknown type. Raises ContractValidationError (or a
        jsonschema.ValidationError for the envelope/data shape itself) — never returns
        a value; a clean return means valid."""
        try:
            instance = json.loads(payload)
        except json.JSONDecodeError as e:
            raise ContractValidationError(f"payload is not valid JSON: {e}") from e

        self._envelope.validate(instance)

        wire_type = instance.get("type")
        data_validator = self._data.get(wire_type)
        if data_validator is None:
            raise ContractValidationError(f"no /contract data schema for type {wire_type!r}")
        data_validator.validate(instance.get("data"))

    def validate_heartbeat(self, envelope: dict[str, Any]) -> None:
        """Validates a full envelope dict against the envelope schema and its data
        against the heartbeat data schema specifically (used when constructing a
        heartbeat before publish, mirroring Go's ValidateHeartbeat)."""
        self._envelope.validate(envelope)
        self._heartbeat.validate(envelope.get("data"))
