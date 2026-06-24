package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
)

// BlindIndex returns a deterministic HMAC-SHA256 of value under key. It enables
// equality lookup (dedup, suppression match) over encrypted columns without
// decrypting. key must be 32 bytes; value should be normalized by the caller
// (e.g. E.164 phone). Never use the master KEK as the blind-index key.
func BlindIndex(key []byte, value string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(value))
	return m.Sum(nil)
}
