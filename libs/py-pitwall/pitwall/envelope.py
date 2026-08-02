"""The standard wire envelope codec, built directly on the /contract-generated
PitwallMessageEnvelope Pydantic model (contract/codegen/python). Unlike the Go library
(which keeps a hand-written Envelope struct — see Story 3.1 Dev Agent Record for why),
Python's generated envelope model IS used directly here: its `occurredAt`/`data` fields
are plain `str`/`dict` (Task 1/3 fixed the codegen traps that would otherwise make
direct use unsafe), so there is no mapping-at-the-edge cost to avoid.

Construction/parsing always goes through the model's camelCase alias (dict literals or
`model_validate`) — never snake_case keyword arguments — so no `populate_by_name` config
is needed (see Task 3 Dev Agent Record, "wire codec convention"). Internal Python code
reads the idiomatic snake_case attributes (`env.master_id`-style) for free.
"""

from __future__ import annotations

import json
import uuid
from datetime import UTC, datetime
from typing import Any

from pitwall_contract.control.control_heartbeat_v1_schema import (
    ControlControlHeartbeatV1DataPayload,
)
from pitwall_contract.envelope_schema import PitwallMessageEnvelope

# HEARTBEAT_ROUTING_KEY is the contract routing key (== envelope `type`) for the
# cross-cutting 1 s liveness signal — identical across every language (Q&A Round 25).
HEARTBEAT_ROUTING_KEY = "control.heartbeat"


def format_wire_time(dt: datetime) -> str:
    """Renders dt as a contract-compliant timestamp string: RFC3339 UTC, exactly
    3-digit milliseconds, literal 'Z' (never '+00:00', never a bare second with no
    fractional part — the exact bug class found for Go's time.Time / Pydantic's
    AwareDatetime in Tasks 1/3)."""
    aware = dt.astimezone(UTC)
    return aware.strftime("%Y-%m-%dT%H:%M:%S.") + f"{aware.microsecond // 1000:03d}Z"


def _new_id() -> str:
    """UUID v7 (time-ordered), lowercase canonical — matches Go's uuid.Must(uuid.NewV7())."""
    return str(uuid.uuid7())


def new_domain_envelope(
    routing_key: str,
    source: str,
    correlation_id: str,
    occurred_at: str,
    data: dict[str, Any],
) -> PitwallMessageEnvelope:
    """Fills the standard envelope for a flow-originating domain event: a fresh
    time-ordered UUID v7 id, the given routing key as `type`, the supplied
    correlationId, a null causationId (flow-originating), and the typed data."""
    return PitwallMessageEnvelope.model_validate(
        {
            "id": _new_id(),
            "type": routing_key,
            "source": source,
            "schemaVersion": 1,
            "envelopeVersion": 1,
            "occurredAt": occurred_at,
            "correlationId": correlation_id,
            "causationId": None,
            "data": data,
        }
    )


def new_caused_envelope(
    routing_key: str,
    source: str,
    correlation_id: str,
    causation_id: str,
    occurred_at: str,
    data: dict[str, Any],
) -> PitwallMessageEnvelope:
    """new_domain_envelope for an event caused by another message: stamps causationId
    with the id of the triggering envelope (never null), preserving the correlationId
    so the whole flow stays linked. Used by reactive handlers (e.g. a privacy.erased
    ack caused by a privacy.erasure_requested)."""
    return PitwallMessageEnvelope.model_validate(
        {
            "id": _new_id(),
            "type": routing_key,
            "source": source,
            "schemaVersion": 1,
            "envelopeVersion": 1,
            "occurredAt": occurred_at,
            "correlationId": correlation_id,
            "causationId": causation_id,
            "data": data,
        }
    )


def new_heartbeat_envelope(
    service: str,
    instance_id: str,
    correlation_id: str,
    now: datetime,
) -> PitwallMessageEnvelope:
    """Builds a fully-populated control.heartbeat envelope. The heartbeat is
    flow-originating, so causationId is null and correlationId is the service's
    lifecycle id. occurredAt and data.at are both stamped from now."""
    ts = format_wire_time(now)
    payload = ControlControlHeartbeatV1DataPayload.model_validate(
        {"service": service, "at": ts, "instanceId": instance_id}
    )
    return new_domain_envelope(
        routing_key=HEARTBEAT_ROUTING_KEY,
        source=service,
        correlation_id=correlation_id,
        occurred_at=ts,
        data=payload.model_dump(mode="json", by_alias=True, exclude_none=False),
    )


def decode_incoming(payload: bytes) -> PitwallMessageEnvelope:
    """Parses raw bytes into a PitwallMessageEnvelope. Does NOT validate against
    /contract (call the Validator first — validate-on-consume). `data` stays a plain
    dict so the caller can decode it into its own typed payload only once the
    envelope's `type` is known (tolerant reader), mirroring Go's
    IncomingEnvelope/DecodeIncoming split."""
    raw = json.loads(payload)
    return PitwallMessageEnvelope.model_validate(raw)
