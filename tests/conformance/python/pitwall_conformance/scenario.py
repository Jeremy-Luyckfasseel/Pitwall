"""Loads tests/conformance/scenarios/*.yaml — the SAME language-neutral spec the Go
runner reads (mirrors tests/conformance/go/scenario.go's Scenario/LoadScenario). This
module only decodes the fields the Python runner actually needs (currently just the
heartbeat scenario); it is not a full port of every Go field.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import yaml


@dataclass(frozen=True)
class HeartbeatSpec:
    interval_ms: int
    min_count: int
    window_ms: int


@dataclass(frozen=True)
class ExpectSpec:
    graceful_shutdown: bool


@dataclass(frozen=True)
class Scenario:
    name: str
    title: str
    quarantine: bool
    heartbeat: HeartbeatSpec | None
    expect: ExpectSpec


def repo_root() -> Path:
    d = Path(__file__).resolve()
    while d != d.parent:
        if (d / "tests" / "conformance" / "scenarios").is_dir():
            return d
        d = d.parent
    raise RuntimeError("could not locate repo root (tests/conformance/scenarios)")


def load_scenario(name: str) -> Scenario:
    """Loads one named scenario from tests/conformance/scenarios/<name>.yaml."""
    path = repo_root() / "tests" / "conformance" / "scenarios" / f"{name}.yaml"
    raw = yaml.safe_load(path.read_text(encoding="utf-8"))

    hb = raw.get("heartbeat")
    heartbeat = (
        HeartbeatSpec(interval_ms=hb["intervalMs"], min_count=hb["minCount"], window_ms=hb["windowMs"])
        if hb
        else None
    )
    expect_raw = raw.get("expect", {})
    expect = ExpectSpec(graceful_shutdown=bool(expect_raw.get("gracefulShutdown", False)))

    return Scenario(
        name=raw["name"],
        title=raw.get("title", ""),
        quarantine=bool(raw.get("quarantine", False)),
        heartbeat=heartbeat,
        expect=expect,
    )
