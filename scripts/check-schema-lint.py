#!/usr/bin/env python3
"""Schema-lint: structural gate on the /contract JSON Schemas (the lint-pass of the CI
contract-test layer, distinct from the example-pass in validate-contract.py).

Enforces the tolerant-reader rule (architecture AR9/AR12, contract/README.md):
  - Every contract/schemas/**/*.schema.json MUST be valid JSON and a valid
    Draft 2020-12 JSON Schema.
  - NO schema object may set `additionalProperties: false` — anywhere, at any depth.
    That would break additive evolution (a new optional field would be rejected by
    existing consumers). Tolerant readers ignore unknown fields.

Run:
  python scripts/check-schema-lint.py            # lint the real tree
  python scripts/check-schema-lint.py --selftest # prove the linter catches a violation

Exits non-zero if any schema is malformed or sets additionalProperties:false.
Requires: pip install jsonschema
"""
import glob
import json
import os
import sys

from jsonschema import Draft202012Validator

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCHEMAS_GLOB = "contract/schemas/**/*.schema.json"


def load(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def find_additional_properties_false(node, path="$"):
    """Walk a JSON Schema document; yield JSON-pointer-ish paths where
    additionalProperties is literally False."""
    if isinstance(node, dict):
        for key, value in node.items():
            here = f"{path}.{key}"
            if key == "additionalProperties" and value is False:
                yield here
            else:
                yield from find_additional_properties_false(value, here)
    elif isinstance(node, list):
        for i, item in enumerate(node):
            yield from find_additional_properties_false(item, f"{path}[{i}]")


def lint_schema_doc(rel, doc):
    """Return a list of error strings for one already-parsed schema document."""
    errors = []
    try:
        Draft202012Validator.check_schema(doc)
    except Exception as exc:  # jsonschema.exceptions.SchemaError
        errors.append(f"[meta-schema] not a valid Draft 2020-12 schema: {exc}")
    for loc in find_additional_properties_false(doc):
        errors.append(f"[tolerant-reader] additionalProperties:false at {loc} — forbidden")
    return errors


def lint_tree():
    schemas = sorted(glob.glob(os.path.join(ROOT, SCHEMAS_GLOB), recursive=True))
    if not schemas:
        print("No schemas found under contract/schemas/.")
        return 1
    ok = fail = 0
    for path in schemas:
        rel = os.path.relpath(path, ROOT).replace(os.sep, "/")
        try:
            doc = load(path)
        except json.JSONDecodeError as exc:
            print(f"FAIL {rel}\n   - [json] invalid JSON: {exc}")
            fail += 1
            continue
        errors = lint_schema_doc(rel, doc)
        if errors:
            fail += 1
            print(f"FAIL {rel}")
            for e in errors:
                print("   -", e)
        else:
            ok += 1
            print(f"ok   {rel}")
    print(f"\n{ok} passed, {fail} failed")
    return 1 if fail else 0


def selftest():
    """Prove the linter flags a violation (red) and passes a clean doc (green)."""
    bad = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "additionalProperties": False,
        "properties": {"masterId": {"type": "string"}},
    }
    good = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "additionalProperties": True,
        "properties": {"masterId": {"type": "string"}},
    }
    bad_errors = lint_schema_doc("<bad>", bad)
    good_errors = lint_schema_doc("<good>", good)
    failures = []
    if not any("additionalProperties:false" in e for e in bad_errors):
        failures.append("expected the linter to flag additionalProperties:false, it did not")
    if good_errors:
        failures.append(f"expected a clean doc to pass, got: {good_errors}")
    if failures:
        for f in failures:
            print("SELFTEST FAIL:", f)
        return 1
    print("SELFTEST ok: violation detected, clean doc passes")
    return 0


if __name__ == "__main__":
    if "--selftest" in sys.argv[1:]:
        sys.exit(selftest())
    sys.exit(lint_tree())
