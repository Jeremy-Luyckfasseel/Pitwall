"""Driver's racing-profile domain logic (Story 3.2): the MinimalProfile shape and
the driver.profile_updated envelope builder. Pure, I/O-free -- no DB, no broker --
mirroring services/identity/internal/domain/identity.go's style so it stays
unit-testable without fakes for persistence/messaging.
"""

from __future__ import annotations

from dataclasses import dataclass

from pitwall.envelope import new_caused_envelope
from pitwall_contract.driver.driver_profile_updated_v1_schema import (
    DriverDriverProfileUpdatedV1DataPayload,
)

# PROFILE_UPDATED_ROUTING_KEY is the contract routing key (== envelope `type`) for
# Driver's racing-profile-changed fact (contract/schemas/driver/driver.profile_updated.v1.schema.json).
PROFILE_UPDATED_ROUTING_KEY = "driver.profile_updated"


@dataclass(frozen=True)
class MinimalProfile:
    """Driver's racing identity/preference fields for one masterId. All three racing
    fields default to unset (None) -- the minimal-profile safety net (FR12) only ever
    creates a profile in this all-unset shape; a value is filled in later by whatever
    future path actually edits it (out of this story's scope, Q&A Round 35/Q35.2)."""

    master_id: str
    racing_number: str | None = None
    kart_class: str | None = None
    nickname: str | None = None


def build_profile_updated(
    source: str,
    correlation_id: str,
    causation_id: str,
    occurred_at: str,
    profile: MinimalProfile,
):
    """Builds the driver.profile_updated envelope for `profile`. Always CAUSED
    (never flow-originating): this fact is a reaction to the specific lap.recorded /
    identity.resolved envelope that triggered profile creation, so causation_id must
    be that triggering envelope's id, never null (mirrors pitwall.erasure.Handler's
    use of new_caused_envelope for its own reactive privacy.erased ack)."""
    payload = DriverDriverProfileUpdatedV1DataPayload.model_validate(
        {
            "masterId": profile.master_id,
            "racingNumber": profile.racing_number,
            "kartClass": profile.kart_class,
            "nickname": profile.nickname,
        }
    )
    return new_caused_envelope(
        routing_key=PROFILE_UPDATED_ROUTING_KEY,
        source=source,
        correlation_id=correlation_id,
        causation_id=causation_id,
        occurred_at=occurred_at,
        data=payload.model_dump(mode="json", by_alias=True, exclude_none=False),
    )
