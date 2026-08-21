"""Driver's canonical-PR domain logic (Story 3.4): the driver.pr_updated envelope
builder. Pure, I/O-free -- no DB, no broker -- mirroring driver.domain.history's style so
it stays unit-testable without fakes.

Driver is the system of record for the all-time PR (FR11). driver.pr_updated is emitted
when Driver confirms a NEW canonical PR (recomputed from its own lap history, Q&A Round 37
/ Q37.4); Timing refreshes its local copy from it.
"""

from __future__ import annotations

from pitwall.envelope import new_caused_envelope
from pitwall_contract.driver.driver_pr_updated_v1_schema import (
    DriverDriverPrUpdatedV1DataPayload,
)

# PR_UPDATED_ROUTING_KEY is the contract routing key (== envelope `type`) for Driver's
# confirmed-canonical-PR fact (contract/schemas/driver/driver.pr_updated.v1.schema.json).
PR_UPDATED_ROUTING_KEY = "driver.pr_updated"


def build_pr_updated(
    source: str,
    correlation_id: str,
    causation_id: str,
    occurred_at: str,
    master_id: str,
    lap_time_ms: int,
    set_at: str,
):
    """Builds the driver.pr_updated envelope for a confirmed canonical PR. Always CAUSED
    (never flow-originating): this fact reacts to the specific personal_record.broken
    envelope that triggered the confirmation, so causation_id must be that triggering
    envelope's id, never null (mirrors build_history_appended). set_at is the wire time of
    the record-setting lap."""
    payload = DriverDriverPrUpdatedV1DataPayload.model_validate(
        {
            "masterId": master_id,
            "lapTimeMs": lap_time_ms,
            "setAt": set_at,
        }
    )
    return new_caused_envelope(
        routing_key=PR_UPDATED_ROUTING_KEY,
        source=source,
        correlation_id=correlation_id,
        causation_id=causation_id,
        occurred_at=occurred_at,
        data=payload.model_dump(mode="json", by_alias=True, exclude_none=False),
    )
