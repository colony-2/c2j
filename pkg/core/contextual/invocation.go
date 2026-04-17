package contextual

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Invocation captures deterministic identifiers for an op execution.
type Invocation struct {
	NodePath  string `json:"path"`
	InvokeSeq int64  `json:"sequence"`
}

// Hash returns a truncated hex-encoded SHA-256 hash over deterministic invocation fields.
func GetInvocationHash(inv Invocation) string {
	hasher := sha256.New()
	hasher.Write([]byte(inv.NodePath))
	hasher.Write([]byte{0})
	hasher.Write([]byte(strconv.FormatInt(inv.InvokeSeq, 10)))
	sum := hasher.Sum(nil)
	return hex.EncodeToString(sum[:8])
}
