package messaging

import (
	libmsg "github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
)

// Validator validates messages against the /contract JSON Schemas. The mechanics
// live once in libs/go-pitwall/messaging; this is Leaderboard's re-export. Here it is
// used to validate-on-CONSUME (the mirror of Timing's validate-on-publish): an invalid
// message is logged + rejected by the caller, never applied to the read-model
// (CLAUDE.md rule 5 "invalid -> log + dead-letter").
type Validator = libmsg.Validator

// ResolveContractDir returns the directory holding the /contract tree.
func ResolveContractDir(explicit string) (string, error) { return libmsg.ResolveContractDir(explicit) }

// NewValidator compiles the schemas needed to validate this service's messages.
func NewValidator(contractDir string) (*Validator, error) { return libmsg.NewValidator(contractDir) }
