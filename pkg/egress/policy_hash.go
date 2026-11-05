package egress

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

// PolicyHashPrefix marks the hash algorithm in a policy revision so a stored
// hash can be compared with a newly computed one without guessing.
const PolicyHashPrefix = "sha256:"

// PolicyHash returns a stable SHA-256 digest of everything in the policy that
// can change a decision: the default action and each rule's identity, match
// mode, action, target, reason, names, and address constraints. Rule order is
// significant, exactly as it is for evaluation.
//
// The hash is independent of Generation. Generation is a compact content
// identifier that decisions carry; a caller that stamps a policy with an
// explicit generation does not change what the policy permits, and must not
// change its hash either. A policy revision store uses this digest to tell
// "the same declaration was applied again" from "the declaration changed".
func PolicyHash(policy Policy) string {
	h := sha256.New()
	writeHashField(h, string(policy.defaultAction))
	for _, rule := range policy.rules {
		writeHashField(h, rule.ID)
		// Match is always written so an endpoint rule and an exact rule with a
		// name that reads like a match mode cannot collide.
		writeHashField(h, string(rule.Match))
		writeHashField(h, string(rule.Action))
		writeHashField(h, rule.Target)
		writeHashField(h, rule.Reason)
		writeHashField(h, "\x00names")
		for _, name := range rule.Names {
			writeHashField(h, name)
		}
		writeHashField(h, "\x00allowed_ips")
		for _, address := range rule.AllowedIPs {
			writeHashField(h, address)
		}
		writeHashField(h, "\x00rule")
	}
	return PolicyHashPrefix + hex.EncodeToString(h.Sum(nil))
}

func writeHashField(h hash.Hash, value string) {
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(value)))
	// hash.Hash never fails a write; the error is always nil.
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}
