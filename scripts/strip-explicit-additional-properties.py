#!/usr/bin/env python3
"""One-time migration helper (Story 3.1): remove explicit `"additionalProperties": true`
lines from every /contract schema, at any depth, WITHOUT reformatting anything else
(preserves exact byte-for-byte line endings and array/object style elsewhere in the file).

Semantically a no-op: JSON Schema's default is already `true`; schema-lint only forbids
an explicit `false`, never required an explicit `true`. But go-jsonschema treats an
EXPLICIT `additionalProperties: true` differently from an absent key — it emits an
untagged `AdditionalProperties interface{}` capture field that corrupts json.Marshal
output (no `json:"-"` tag). Dropping the explicit keyword makes the generated Go structs
directly usable. Not meant to be re-run routinely; kept for reviewability of the diff.
"""
import re
import sys
from pathlib import Path

# Line has a trailing comma (not the object's last property) -> just delete the line.
WITH_COMMA = re.compile(r'[ \t]*"additionalProperties":\s*true,[ \t]*\r?\n')
# Line has NO trailing comma (it IS the object's last property) -> delete the line AND
# strip the trailing comma from the preceding property line, so the result stays valid JSON.
# Group 1 keeps only the newline that ended the preceding line (dropping its comma); the
# additionalProperties line's own indentation is consumed (matched) but NOT replayed, so
# whatever line follows keeps its own original indentation untouched.
WITHOUT_COMMA = re.compile(r',([ \t]*\r?\n)[ \t]*"additionalProperties":\s*true[ \t]*\r?\n')


def main():
    root = Path(__file__).resolve().parent.parent / "contract" / "schemas"
    changed = []
    for path in sorted(root.rglob("*.schema.json")):
        with open(path, "r", encoding="utf-8", newline="") as f:
            text = f.read()
        new_text = WITHOUT_COMMA.sub(r"\1", text)
        new_text = WITH_COMMA.sub("", new_text)
        if new_text != text:
            with open(path, "w", encoding="utf-8", newline="") as f:
                f.write(new_text)
            changed.append(str(path.relative_to(root.parent.parent)))
    for p in changed:
        print(f"stripped: {p}")
    print(f"{len(changed)} schema file(s) updated")


if __name__ == "__main__":
    sys.exit(main())
