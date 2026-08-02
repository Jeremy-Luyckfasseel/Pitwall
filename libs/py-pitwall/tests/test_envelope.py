import json
from datetime import UTC, datetime

from pitwall.envelope import (
    HEARTBEAT_ROUTING_KEY,
    decode_incoming,
    format_wire_time,
    new_caused_envelope,
    new_domain_envelope,
    new_heartbeat_envelope,
)


def test_format_wire_time_exact_millis_and_z():
    dt = datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC)
    assert format_wire_time(dt) == "2026-06-02T14:03:21.512Z"


def test_format_wire_time_whole_second_still_has_three_digit_millis():
    # The exact bug class found for Go's time.Time / Pydantic's AwareDatetime
    # (Task 1/3): a whole-second value must NOT drop the fractional part.
    dt = datetime(2026, 6, 2, 14, 3, 21, 0, tzinfo=UTC)
    assert format_wire_time(dt) == "2026-06-02T14:03:21.000Z"


def test_new_domain_envelope_is_flow_originating():
    env = new_domain_envelope(
        routing_key="lap.recorded",
        source="timing",
        correlation_id="8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
        occurred_at="2026-06-02T14:03:21.512Z",
        data={"masterId": "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"},
    )
    assert env.type == "lap.recorded"
    assert env.source == "timing"
    assert env.schema_version == 1
    assert env.envelope_version == 1
    assert env.causation_id is None
    assert env.correlation_id == "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55"
    # UUID v7, lowercase canonical
    assert len(env.id) == 36
    assert env.id == env.id.lower()

    dumped = env.model_dump(mode="json", by_alias=True, exclude_none=False)
    assert dumped["causationId"] is None  # present key, not omitted
    assert dumped["id"] == env.id


def test_new_caused_envelope_carries_causation_id():
    correlation_id = "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55"
    causation_id = "aa11bb22-cc33-4dd4-8ee5-ff6677889900"
    env = new_caused_envelope(
        routing_key="privacy.erased",
        source="driver",
        correlation_id=correlation_id,
        causation_id=causation_id,
        occurred_at="2026-06-02T14:03:21.512Z",
        data={
            "requestId": "aa11bb22-cc33-4dd4-8ee5-ff6677889900",
            "masterId": "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
            "service": "driver",
            "mode": "deleted",
            "at": "2026-06-02T14:03:21.512Z",
        },
    )
    assert env.causation_id == causation_id
    assert env.correlation_id == correlation_id


def test_new_heartbeat_envelope():
    now = datetime(2026, 6, 2, 14, 3, 21, 512000, tzinfo=UTC)
    env = new_heartbeat_envelope(
        service="driver",
        instance_id="inst-1",
        correlation_id="8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
        now=now,
    )

    assert env.type == HEARTBEAT_ROUTING_KEY
    assert env.source == "driver"
    assert env.causation_id is None

    dumped = env.model_dump(mode="json", by_alias=True, exclude_none=False)
    assert dumped["data"] == {
        "service": "driver",
        "at": "2026-06-02T14:03:21.512Z",
        "instanceId": "inst-1",
    }


def test_decode_incoming_defers_typed_data_decode():
    raw = json.dumps(
        {
            "id": "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f",
            "type": "lap.recorded",
            "source": "timing",
            "schemaVersion": 1,
            "envelopeVersion": 1,
            "occurredAt": "2026-06-02T14:03:21.512Z",
            "correlationId": "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
            "causationId": None,
            "data": {"masterId": "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"},
        }
    ).encode("utf-8")

    env = decode_incoming(raw)
    assert env.type == "lap.recorded"
    # data stays a raw dict — typed decode happens after `type` is inspected
    assert env.data == {"masterId": "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21"}
