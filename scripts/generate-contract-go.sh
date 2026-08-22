#!/bin/sh
# Generates Go wire DTOs from /contract/schemas into contract/codegen/go/ (Story 3.1,
# AC1). One package per schema namespace directory (+ a standalone `envelope` package
# for the root envelope.schema.json), mirroring contract/schemas/<ns>/ layout.
#
# Flags (all load-bearing — do not drop any of them without re-verifying against a real
# schema, see docs/analysis/00-questions-and-answers.md Round 34 / Story 3.1 Dev Notes):
#   --only-models          generated code is plain structs only (json tags); no custom
#                           Unmarshal/validation methods, so a schema declaring
#                           `additionalProperties: true` does NOT emit an untagged
#                           `AdditionalProperties interface{}` capture field that would
#                           corrupt json.Marshal output (no json:"-" tag on it otherwise).
#   --disable-omitempty
#   --disable-omitzero     the wire contract requires nullable-but-meaningful fields
#                           (e.g. transponderId, causationId) to always serialize as a
#                           present key (null when absent), never be omitted — the
#                           existing hand-written Go code already follows this
#                           convention for every pointer field.
#   --capitalization ID    idiomatic Go initialisms (ID, not Id) to match existing
#                           hand-written code (CausationID, MasterID, ...).
#
# Schemas with a `format: date-time` field generate that field as time.Time, whose
# default JSON marshaling does NOT reliably reproduce the pinned exactly-3-digit-millis
# wire format (a whole-second value drops the fractional part entirely). Every schema
# these DTOs are used for on the publish path has already had the (non-enforced,
# decorative — "the pattern is enforced, format is annotation-only") `format` keyword
# removed, so those fields generate as plain `string` instead (Story 3.1, Task 1).
set -e
cd "$(dirname "$0")/.."

GO_JSONSCHEMA="github.com/atombender/go-jsonschema@v0.24.1"
FLAGS="--only-models --tags json --disable-omitempty --disable-omitzero --capitalization ID"
OUT=contract/codegen/go
SCHEMAS=contract/schemas

mkdir -p "$OUT/envelope" "$OUT/bar" "$OUT/billing" "$OUT/control" "$OUT/driver" "$OUT/frontend" "$OUT/identity" "$OUT/privacy" "$OUT/timing"

echo "generating contract/codegen/go/envelope ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p envelope \
  "$SCHEMAS/envelope.schema.json" \
  -o "$OUT/envelope/envelope.go"

echo "generating contract/codegen/go/bar ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p bar \
  "$SCHEMAS/bar/bar.order_placed.v1.schema.json" \
  -o "$OUT/bar/types.go"

echo "generating contract/codegen/go/billing ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p billing \
  "$SCHEMAS/billing/giftcard.issued.v1.schema.json" \
  "$SCHEMAS/billing/giftcard.redeemed.v1.schema.json" \
  "$SCHEMAS/billing/wallet.debited.v1.schema.json" \
  "$SCHEMAS/billing/wallet.topped_up.v1.schema.json" \
  -o "$OUT/billing/types.go"

echo "generating contract/codegen/go/control ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p control \
  "$SCHEMAS/control/control.heartbeat.v1.schema.json" \
  "$SCHEMAS/control/privacy.export_ready.v1.schema.json" \
  -o "$OUT/control/types.go"

echo "generating contract/codegen/go/driver ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p driver \
  "$SCHEMAS/driver/driver.history_appended.v1.schema.json" \
  "$SCHEMAS/driver/driver.pr_updated.v1.schema.json" \
  "$SCHEMAS/driver/driver.profile_updated.v1.schema.json" \
  -o "$OUT/driver/types.go"

echo "generating contract/codegen/go/frontend ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p frontend \
  "$SCHEMAS/frontend/identity.lookup_requested.v1.schema.json" \
  "$SCHEMAS/frontend/payment.captured.v1.schema.json" \
  "$SCHEMAS/frontend/privacy.erasure_requested.v1.schema.json" \
  "$SCHEMAS/frontend/privacy.export_requested.v1.schema.json" \
  "$SCHEMAS/frontend/profile.edit_requested.v1.schema.json" \
  "$SCHEMAS/frontend/schedule.change_requested.v1.schema.json" \
  "$SCHEMAS/frontend/session.control_requested.v1.schema.json" \
  -o "$OUT/frontend/types.go"

echo "generating contract/codegen/go/identity ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p identity \
  "$SCHEMAS/identity/identity.resolved.v1.schema.json" \
  -o "$OUT/identity/types.go"

echo "generating contract/codegen/go/privacy ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p privacy \
  "$SCHEMAS/privacy/privacy.data_provided.v1.schema.json" \
  "$SCHEMAS/privacy/privacy.erased.v1.schema.json" \
  -o "$OUT/privacy/types.go"

echo "generating contract/codegen/go/timing ..."
GOWORK=off go run $GO_JSONSCHEMA $FLAGS -p timing \
  "$SCHEMAS/timing/driver.checked_in.v1.schema.json" \
  "$SCHEMAS/timing/lap.recorded.v1.schema.json" \
  "$SCHEMAS/timing/personal_record.broken.v1.schema.json" \
  "$SCHEMAS/timing/scanner.offline.v1.schema.json" \
  "$SCHEMAS/timing/scanner.online.v1.schema.json" \
  "$SCHEMAS/timing/session.ended.v1.schema.json" \
  "$SCHEMAS/timing/session.started.v1.schema.json" \
  -o "$OUT/timing/types.go"

echo "done."
