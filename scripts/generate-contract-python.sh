#!/bin/sh
# Generates Python (Pydantic v2) wire DTOs from /contract/schemas into
# contract/codegen/python/pitwall_contract/ (Story 3.1, AC1). One module per schema
# file, mirroring contract/schemas/<ns>/ (directory mode preserves the tree and emits
# __init__.py per package automatically).
#
# Flags (all load-bearing — do not drop any without re-verifying against a real
# schema, see docs/analysis/00-questions-and-answers.md Round 34 / Story 3.1 Dev Notes):
#   --type-mappings "string+uuid=string"
#       Without this, a `format: "uuid"` field maps to Python's native UUID type,
#       which SILENTLY DROPS any accompanying `pattern` (e.g. masterId's
#       version-4-specific regex `4[0-9a-f]{3}-[89ab][0-9a-f]{3}...` would be
#       discarded, accepting ANY UUID version) and would ACCEPT brace/urn UUID forms
#       our wire rules explicitly reject (readers must reject them, not normalize).
#       Verified against a real datamodel-code-generator run before landing this.
#   --type-mappings "string+date-time=string"
#       Without this, a `format: "date-time"` field maps to Pydantic's AwareDatetime.
#       Its default JSON serialization does not reliably reproduce the pinned
#       exactly-3-digit-millis wire format — the same class of bug already found and
#       fixed for Go's time.Time in Task 1 (there, fixed by dropping the schema
#       keyword on in-scope schemas; here, fixed generator-wide since the flag makes
#       it free to apply to every schema, not just the ones this story touches).
#   --snake-case-field
#       Wire fields stay camelCase (via a per-field Pydantic `alias`); Python
#       attribute names become idiomatic snake_case. Construction/parsing use the
#       alias (camelCase) by default — matches how a JSON payload naturally looks;
#       internal Python code reads/writes the snake_case attribute. No
#       `populate_by_name` needed: nothing in this codebase constructs these models
#       via snake_case keyword arguments (see the "Wire codec convention" note in the
#       story's Dev Agent Record for Task 3).
#   --output-model-type pydantic_v2.BaseModel / --target-pydantic-version 2
#       Pinned per architecture (Python 3.14.x, Pydantic v2).
set -e
cd "$(dirname "$0")/.."

PKG_ROOT=contract/codegen/python/pitwall_contract
rm -rf "$PKG_ROOT"

python3 -m datamodel_code_generator \
  --input contract/schemas \
  --input-file-type jsonschema \
  --output "$PKG_ROOT" \
  --output-model-type pydantic_v2.BaseModel \
  --target-python-version 3.14 \
  --target-pydantic-version 2 \
  --type-mappings "string+uuid=string" "string+date-time=string" \
  --snake-case-field \
  --formatters builtin

echo "done."
