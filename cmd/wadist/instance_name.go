package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// instanceNameFor derives a stable, unique Evolution instance name for an
// account. Deterministic: same (tenant,node,jid) always yields the same name.
func instanceNameFor(tenantID int64, node, jid string) string {
	h := sha256.Sum256([]byte(jid))
	return fmt.Sprintf("wa_%d_%s_%s", tenantID, node, hex.EncodeToString(h[:])[:12])
}
