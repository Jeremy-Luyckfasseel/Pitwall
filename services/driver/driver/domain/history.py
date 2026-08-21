"""Driver's lap-history / session-summary domain logic (Story 3.3): the
driver.history_appended envelope builder. Pure, I/O-free -- no DB, no broker --
mirroring driver.domain.profile's style so it stays unit-testable without fakes.

One driver.history_appended is emitted PER driver row of a consumed session.ended
summary (Q&A Round 36 / Q36.2); this module builds a single such envelope.
"""

from __future__ import annotations

from pitwall.envelope import new_caused_envelope
from pitwall_contract.driver.driver_history_appended_v1_schema import (
    DriverDriverHistoryAppendedV1DataPayload,
)

# HISTORY_APPENDED_ROUTING_KEY is the contract routing key (== envelope `type`) for
# Driver's per-session-result-stored fact
# (contract/schemas/driver/driver.history_appended.v1.schema.json).
HISTORY_APPENDED_ROUTING_KEY = "driver.history_appended"


def build_history_appended(
    source: str,
    correlation_id: str,
    causation_id: str,
    occurred_at: str,
    master_id: str,
    session_id: str,
    best_lap_ms: int | None,
    lap_count: int | None,
):
    """Builds the driver.history_appended envelope for one driver's stored session
    result. Always CAUSED (never flow-originating): this fact reacts to the specific
    session.ended envelope that carried the summary row, so causation_id must be that
    triggering envelope's id, never null (mirrors build_profile_updated). bestLapMs /
    lapCount may be None -- they serialize as present null keys (no omitempty, AR9)."""
    payload = DriverDriverHistoryAppendedV1DataPayload.model_validate(
        {
            "masterId": master_id,
            "sessionId": session_id,
            "bestLapMs": best_lap_ms,
            "lapCount": lap_count,
        }
    )
    return new_caused_envelope(
        routing_key=HISTORY_APPENDED_ROUTING_KEY,
        source=source,
        correlation_id=correlation_id,
        causation_id=causation_id,
        occurred_at=occurred_at,
        data=payload.model_dump(mode="json", by_alias=True, exclude_none=False),
    )
