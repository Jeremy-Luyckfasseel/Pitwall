"""Reusable right-to-be-forgotten mechanics every Pitwall service shares (DG-3/DG-7,
ADR-0009): on a validated privacy.erasure_requested it runs, atomically, inbox-dedupe
-> the service's delete-slice -> a tombstone write -> inbox-mark, then builds the
privacy.erased acknowledgement to emit. Also offers a tombstone guard so a replayed
event cannot resurrect an erased slice. Mirrors libs/go-pitwall/erasure/erasure.go.

Carries mechanics ONLY: each service injects its own delete-slice and tombstone
callbacks (it owns those tables); this module hard-codes no schema.
"""

from __future__ import annotations

import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime
from enum import StrEnum

from pitwall.envelope import format_wire_time, new_caused_envelope
from pitwall.persistence import inbox_has_seen, inbox_mark_seen, within_tx

ERASURE_REQUESTED_TYPE = "privacy.erasure_requested"
ERASED_TYPE = "privacy.erased"


class Mode(StrEnum):
    """How a service honored erasure: DELETED = local slice removed; ANONYMIZED = a
    retained record stripped of PII (e.g. Billing, under legal retention)."""

    DELETED = "deleted"
    ANONYMIZED = "anonymized"


@dataclass(frozen=True)
class Result:
    """Reports the outcome of handling one erasure request."""

    request_id: str
    master_id: str
    duplicate: bool  # the envelope id was already processed (a no-op redelivery)
    ack: object | None = None  # the privacy.erased envelope to emit; None on a duplicate


# SliceDeleter deletes/anonymizes the service's OWN slice for master_id, inside the
# caller's transaction (conn is the open sqlite3.Connection, already inside within_tx).
SliceDeleter = Callable[[sqlite3.Connection, str], None]

# TombstoneWriter records a tombstone for master_id inside the caller's transaction.
TombstoneWriter = Callable[[sqlite3.Connection, str], None]

# TombstoneChecker reports whether master_id is tombstoned (reads the service's own
# tombstone table; used by guarded_create).
TombstoneChecker = Callable[[str], bool]

# AckEnqueuer durably enqueues the privacy.erased ack INSIDE the caller's transaction
# (via pitwall.persistence.outbox_enqueue against the service's own outbox), so the
# ack commits atomically with the delete + tombstone. Required — without it a crash
# between commit and an out-of-band publish would permanently lose the ack.
AckEnqueuer = Callable[[sqlite3.Connection, object], None]


class Handler:
    """Orchestrates the reusable erasure mechanics for one service."""

    def __init__(
        self,
        db: sqlite3.Connection,
        service: str,
        delete: SliceDeleter,
        tombstone: TombstoneWriter,
        enqueue: AckEnqueuer,
        mode: Mode = Mode.DELETED,
        now: Callable[[], datetime] | None = None,
    ):
        self.db = db
        self.service = service
        self.delete = delete
        self.tombstone = tombstone
        self.enqueue = enqueue
        self.mode = mode
        self._now = now or datetime.now

    def handle(self, envelope_id: str, correlation_id: str, request_id: str, master_id: str) -> Result:
        """Consumes one validated privacy.erasure_requested. Within ONE transaction it
        dedupes on the envelope id (idempotent inbox), runs the service's delete-slice,
        writes the tombstone, and enqueues the privacy.erased ack — so a crash can
        neither half-erase nor ack-without-erasing. On first sight it returns the ack
        for the caller to publish (via the outbox relay); on a redelivery it returns
        duplicate=True and no ack. The caller must have already validated the envelope
        against /contract."""
        at = format_wire_time(self._now())
        ack = new_caused_envelope(
            routing_key=ERASED_TYPE,
            source=self.service,
            correlation_id=correlation_id,
            causation_id=envelope_id,
            occurred_at=at,
            data={
                "requestId": request_id,
                "masterId": master_id,
                "service": self.service,
                "mode": self.mode.value,
                "at": at,
            },
        )

        duplicate = False
        with within_tx(self.db):
            if inbox_has_seen(self.db, envelope_id):
                duplicate = True
            else:
                self.delete(self.db, master_id)
                self.tombstone(self.db, master_id)
                # Enqueue the ack INSIDE this same transaction so it commits atomically
                # with the delete + tombstone — a crash after this point either takes
                # everything (including the durably-queued ack) or nothing.
                self.enqueue(self.db, ack)
                inbox_mark_seen(self.db, envelope_id, ERASURE_REQUESTED_TYPE, at)

        if duplicate:
            return Result(request_id=request_id, master_id=master_id, duplicate=True)
        return Result(request_id=request_id, master_id=master_id, duplicate=False, ack=ack)


def guarded_create(check: TombstoneChecker, master_id: str, create: Callable[[], None]) -> bool:
    """Runs create() ONLY if master_id is not tombstoned; returns blocked=True (create
    skipped) if it is. The reusable guard a consumer wraps its slice (re)creation in so
    a late/replayed event cannot silently resurrect an erased id (DG-7). The tombstone
    check + the create should ordinarily share one transaction in the caller."""
    if check(master_id):
        return True
    create()
    return False
