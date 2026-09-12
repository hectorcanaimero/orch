package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashToken returns the hex-encoded SHA-256 digest of a stakeholder token,
// or "" for an empty token — the one sentinel meaning "no token configured"
// everywhere in this package, so callers never need a second check for it.
//
// Only the hash is ever persisted (state.Backend.SetStakeholderToken) or
// compared against (Config.decide): the plaintext exists only in the
// operator's hands and in the single moment `orch dashboard token rotate`
// prints it. This is also what a supplied token is hashed through before
// being compared to the stored/configured hash — see Server's request-time
// resolution in server.go.
func HashToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
