package messaging

import (
	libmsg "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// Validator validates messages against the /contract JSON Schemas. The mechanics
// live once in libs/go-pitwall/messaging; this is Timing's re-export so existing call
// sites (messaging.Validator, messaging.NewValidator, messaging.ResolveContractDir,
// and the v.ValidateHeartbeat / v.ValidateEnvelopeBytes methods) keep working.
type Validator = libmsg.Validator

// ResolveContractDir returns the directory holding the /contract tree.
func ResolveContractDir(explicit string) (string, error) { return libmsg.ResolveContractDir(explicit) }

// NewValidator compiles the schemas needed to validate this service's messages.
func NewValidator(contractDir string) (*Validator, error) { return libmsg.NewValidator(contractDir) }
