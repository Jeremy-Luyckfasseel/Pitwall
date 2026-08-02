"""The Python side of Story 3.1's cross-language skeleton-mechanics scenario (AC4):
reads the SAME tests/conformance/scenarios/heartbeat.yaml the Go runner reads, starts
the REAL Driver process against a real RabbitMQ (testcontainers), observes its
control.heartbeat on the wire, and asserts graceful shutdown — proving the skeleton
mechanics libs/py-pitwall built in Tasks 4/5 are cross-language equivalent to Go's,
not just per-language unit tested.
"""

from testcontainers.community.rabbitmq import RabbitMqContainer

from pitwall_conformance.runner_helpers import (
    assert_graceful_shutdown,
    assert_heartbeat_shape,
    observe_heartbeats,
    start_driver,
)
from pitwall_conformance.scenario import load_scenario


def test_heartbeat_cadence_format_and_graceful_shutdown(tmp_path):
    sc = load_scenario("heartbeat")
    assert sc.heartbeat is not None, "heartbeat scenario missing its `heartbeat:` spec block"

    with RabbitMqContainer("rabbitmq:4.3-management-alpine") as rabbitmq:
        params = rabbitmq.get_connection_params()
        driver = start_driver(params, sc.heartbeat.interval_ms, tmp_path)
        try:
            beats = observe_heartbeats(
                params,
                exchange="driver.events",
                expect_source="driver",
                min_count=sc.heartbeat.min_count,
                window_s=sc.heartbeat.window_ms / 1000.0,
            )
            assert len(beats) >= sc.heartbeat.min_count, (
                f"observed {len(beats)} heartbeats in {sc.heartbeat.window_ms}ms, "
                f"want >= {sc.heartbeat.min_count}\nlog:\n{driver.log_text()}"
            )
            for b in beats:
                assert_heartbeat_shape(b, "driver")

            # Successive beats must carry a fresh `at` (proves the loop is actually
            # ticking, not replaying/caching one stamped envelope) — same check the
            # Go runner makes.
            seen_at = {b["data"]["at"] for b in beats}
            assert len(seen_at) >= 2, f"expected multiple distinct heartbeat timestamps, got {seen_at}"

            if sc.expect.graceful_shutdown:
                assert_graceful_shutdown(driver)
        finally:
            if driver.proc.poll() is None:
                driver.proc.kill()
                driver.proc.wait(timeout=10)
            driver.close()
