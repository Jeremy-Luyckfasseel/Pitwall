#!/usr/bin/env python3
"""Known-bad fixture gate (the NEGATIVE pass of the CI contract-test layer).

The example-pass (`validate-contract.py`) proves valid examples VALIDATE.
This gate proves the other direction — that deliberately-malformed fixtures are
REJECTED — which is what makes two-sided validation real instead of aspirational
(architecture AR12, contract/README.md "a valid example AND a known-bad fixture per event").

For every contract/examples/**/*.invalid.json it:
  - validates the full message against contract/schemas/envelope.schema.json
  - validates its `data` against contract/schemas/<ns>/<event>.vN.schema.json
  - asserts AT LEAST ONE validation error is produced. A fixture that PASSES is a
    FAIL (a positive control: a known-bad fixture that is no longer bad is a defect).

It also enforces fixture PAIRING so a deleted/orphaned fixture fails the build:
  - within PAIRING_NAMESPACES, every <event>.example.json MUST have a sibling
    <event>.invalid.json (and vice-versa);
  - any *.invalid.json without a matching *.example.json is an orphan → FAIL, in
    any namespace.
  - examples in non-paired namespaces that lack an invalid fixture are reported as
    EXEMPT (informational, never silent) — they are slated for their own stories.

Run:
  python scripts/check-invalid-fixtures.py            # gate the real tree
  python scripts/check-invalid-fixtures.py --selftest # prove the gate has teeth

Exits non-zero if any known-bad fixture validates, or any required pair is missing.
Requires: pip install jsonschema
"""
import glob
import json
import os
import sys

from jsonschema import Draft202012Validator

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXAMPLES_DIR = os.path.join(ROOT, "contract/examples")
SCHEMAS_DIR = os.path.join(ROOT, "contract/schemas")

# Namespaces where example<->invalid pairing is enforced. Scoped to the events
# specified for this story (timing); other namespaces ship their invalid fixtures
# in their own epics/stories and are reported EXEMPT until then (never silently).
PAIRING_NAMESPACES = {"timing"}


def load(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def stem(rel, suffix):
    """contract-relative event stem, e.g. 'timing/session.ended.v1'."""
    return rel[: -len(suffix)]


def namespace(event_stem):
    return event_stem.split("/", 1)[0]


def check_tree():
    env_validator = Draft202012Validator(load(os.path.join(SCHEMAS_DIR, "envelope.schema.json")))

    example_paths = sorted(glob.glob(os.path.join(EXAMPLES_DIR, "**/*.example.json"), recursive=True))
    invalid_paths = sorted(glob.glob(os.path.join(EXAMPLES_DIR, "**/*.invalid.json"), recursive=True))

    example_stems = {stem(os.path.relpath(p, EXAMPLES_DIR).replace(os.sep, "/"), ".example.json") for p in example_paths}
    invalid_stems = {stem(os.path.relpath(p, EXAMPLES_DIR).replace(os.sep, "/"), ".invalid.json") for p in invalid_paths}

    ok = fail = 0

    # --- 1. Negative validation: every known-bad fixture MUST be rejected. ---
    if not invalid_paths:
        print("No *.invalid.json fixtures found — the negative gate has nothing to prove.")
        return 1
    for inv_path in invalid_paths:
        rel = os.path.relpath(inv_path, EXAMPLES_DIR).replace(os.sep, "/")
        msg = load(inv_path)
        errors = [f"[envelope] {e.message}" for e in env_validator.iter_errors(msg)]
        schema_path = os.path.join(SCHEMAS_DIR, stem(rel, ".invalid.json") + ".schema.json")
        if os.path.exists(schema_path):
            data_validator = Draft202012Validator(load(schema_path))
            errors += [f"[data] {e.message}" for e in data_validator.iter_errors(msg.get("data", {}))]
        else:
            errors.append(f"[data] MISSING schema {schema_path}")
        if errors:
            ok += 1
            reason = msg.get("_invalidReason", "(no _invalidReason documented)")
            print(f"ok   {rel} - correctly rejected: {errors[0]}")
            if "_invalidReason" not in msg:
                print('     ! add an "_invalidReason" key documenting why this fixture is bad')
            else:
                print(f"     reason: {reason}")
        else:
            fail += 1
            print(f"FAIL {rel} - this known-bad fixture VALIDATED (it is no longer bad). Positive-control failure.")

    # --- 2. Pairing: missing/orphaned fixtures fail the build. ---
    for orphan in sorted(invalid_stems - example_stems):
        fail += 1
        print(f"FAIL {orphan}.invalid.json - orphan: no matching {orphan}.example.json")

    for ev in sorted(example_stems):
        has_invalid = ev in invalid_stems
        if namespace(ev) in PAIRING_NAMESPACES:
            if not has_invalid:
                fail += 1
                print(f"FAIL {ev}.example.json - paired namespace requires a {ev}.invalid.json (missing)")
        elif not has_invalid:
            print(f"EXEMPT {ev} - no invalid fixture yet (namespace '{namespace(ev)}' not in PAIRING_NAMESPACES)")

    print(f"\n{ok} rejected-as-expected, {fail} failed")
    return 1 if fail else 0


def selftest():
    """Prove the gate flags a fixture that should fail but doesn't (positive control)
    and accepts a fixture that correctly fails."""
    env_validator = Draft202012Validator(load(os.path.join(SCHEMAS_DIR, "envelope.schema.json")))
    lap_schema = Draft202012Validator(load(os.path.join(SCHEMAS_DIR, "timing/lap.recorded.v1.schema.json")))

    # A correctly-bad message: lapTimeMs is a float → data schema rejects it.
    bad = {
        "id": "9f1c2b4e-7a3d-4c8e-bf21-5d0a6e2f1a90",
        "type": "lap.recorded", "source": "timing", "schemaVersion": 1, "envelopeVersion": 1,
        "occurredAt": "2026-05-31T14:03:21.512Z",
        "correlationId": "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55", "causationId": None,
        "data": {"masterId": "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21",
                 "sessionId": "s", "lapNumber": 7, "lapTimeMs": 1.5, "at": "2026-05-31T14:03:21.500Z"},
    }
    # A message that fully validates → MUST be flagged if filed as a known-bad fixture.
    good = dict(bad)
    good["data"] = dict(bad["data"]); good["data"]["lapTimeMs"] = 42318

    def errors_for(msg):
        errs = list(env_validator.iter_errors(msg)) + list(lap_schema.iter_errors(msg["data"]))
        return errs

    failures = []
    if not errors_for(bad):
        failures.append("expected the float fixture to be rejected; it validated")
    if errors_for(good):
        failures.append("expected the clean message to validate (so the gate would FLAG it as a non-failing fixture); it errored")
    if failures:
        for f in failures:
            print("SELFTEST FAIL:", f)
        return 1
    print("SELFTEST ok: bad fixture rejected, clean message validates (would be flagged as a non-failing known-bad fixture)")
    return 0


if __name__ == "__main__":
    if "--selftest" in sys.argv[1:]:
        sys.exit(selftest())
    sys.exit(check_tree())
