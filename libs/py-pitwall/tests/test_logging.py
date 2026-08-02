import io
import json

from pitwall.logging import new_logger


def test_logs_structured_json_with_mandatory_fields():
    buf = io.StringIO()
    log = new_logger(service="driver", correlation_id="c-1", level="info", stream=buf)

    log.info("hello world")

    line = buf.getvalue().strip()
    payload = json.loads(line)
    assert payload["service"] == "driver"
    assert payload["correlationId"] == "c-1"
    assert payload["message"] == "hello world"
    assert payload["level"] == "info"
    assert payload["timestamp"].endswith("Z")
    # exactly 3-digit milliseconds, literal Z (matches the wire timestamp pattern)
    import re

    assert re.match(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$", payload["timestamp"])


def test_extra_fields_are_included():
    buf = io.StringIO()
    log = new_logger(service="driver", correlation_id="c-1", level="info", stream=buf)

    log.info("lap recorded", eventId="abc-123", lapNumber=7)

    payload = json.loads(buf.getvalue().strip())
    assert payload["eventId"] == "abc-123"
    assert payload["lapNumber"] == 7


def test_debug_level_suppressed_below_configured_level():
    buf = io.StringIO()
    log = new_logger(service="driver", correlation_id="c-1", level="info", stream=buf)

    log.debug("should not appear")

    assert buf.getvalue() == ""


def test_unknown_level_falls_back_to_info():
    buf = io.StringIO()
    log = new_logger(service="driver", correlation_id="c-1", level="not-a-real-level", stream=buf)

    log.info("shown")
    log.debug("not shown")

    lines = [l for l in buf.getvalue().splitlines() if l]
    assert len(lines) == 1
    assert json.loads(lines[0])["message"] == "shown"
