// Package heartbeat is Identity's facade over the shared 1 s liveness emitter in
// libs/go-pitwall/heartbeat (Story 2.1). The emit loop + liveness touch-file mechanics
// live once in the library; this re-exports them so Identity's main constructs
// heartbeat.Emitter unchanged (it supplies its own Build/Validate/Publish).
package heartbeat

import (
	libhb "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/heartbeat"
)

// Emitter ticks every Interval, building → validating → publishing a heartbeat and
// touching the liveness file.
type Emitter = libhb.Emitter

// Builder builds a heartbeat envelope stamped at the given time.
type Builder = libhb.Builder

// Validator validates an envelope against /contract; non-nil means do not publish.
type Validator = libhb.Validator

// Publisher publishes a body to a routing key on the owned exchange.
type Publisher = libhb.Publisher
