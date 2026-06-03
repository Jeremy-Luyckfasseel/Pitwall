#!/usr/bin/env python3
"""Contract example validator (the example-pass of the CI contract-test layer).

For every contract/examples/<ns>/<event>.vN.example.json:
  - validate the full message against contract/schemas/envelope.schema.json
  - validate its `data` against contract/schemas/<ns>/<event>.vN.schema.json

Exits non-zero if any example fails or is missing its data schema.
Requires: pip install jsonschema
"""
import glob
import json
import os
import sys

from jsonschema import Draft202012Validator

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def load(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def main():
    env_validator = Draft202012Validator(load(os.path.join(ROOT, "contract/schemas/envelope.schema.json")))
    examples = sorted(glob.glob(os.path.join(ROOT, "contract/examples/**/*.example.json"), recursive=True))
    if not examples:
        print("No examples found.")
        return 1

    ok = fail = 0
    for ex_path in examples:
        rel = os.path.relpath(ex_path, os.path.join(ROOT, "contract/examples")).replace(os.sep, "/")
        schema_path = os.path.join(ROOT, "contract/schemas", rel.replace(".example.json", ".schema.json"))
        msg = load(ex_path)
        errors = [f"[envelope] {e.message}" for e in env_validator.iter_errors(msg)]
        if os.path.exists(schema_path):
            data_validator = Draft202012Validator(load(schema_path))
            errors += [f"[data] {e.message}" for e in data_validator.iter_errors(msg.get("data", {}))]
        else:
            errors.append(f"[data] MISSING schema {schema_path}")
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


if __name__ == "__main__":
    sys.exit(main())
