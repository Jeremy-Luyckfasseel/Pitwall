package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashEmail returns a deterministic, irreversible SHA-256 hash (lowercase hex) of its
// input — the erasure suppression key (Round 33/Q33.1). It lets Identity recognize a
// later identity.lookup_requested for an already-erased address without retaining the
// plaintext email erasure was supposed to remove.
//
// HashEmail does NOT normalize its input — the caller must already have applied
// NormalizeEmail (Round 31) so the SAME hash is produced whether the caller is erasing
// (persistence.ErasureStore.DeleteSlice's caller) or looking up
// (consumer.TxResolver.Resolve), both of which normalize before touching the store.
func HashEmail(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])
}
