package runs

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

type runEventIdentityKind byte

const (
	runEventIdentityFormatVersion byte = 1
	attachedEventExplicitToken    byte = 1
	attachedEventDerivedToken     byte = 2

	// These values participate in persisted event IDs. Keep existing values stable.
	runEventIdentityInitialPrompt  runEventIdentityKind = 1
	runEventIdentityAttachedHuman  runEventIdentityKind = 2
	runEventIdentityAttachedAgent  runEventIdentityKind = 3
	runEventIdentityTerminalAgent  runEventIdentityKind = 4
	runEventIdentityTerminalStatus runEventIdentityKind = 5
)

func initialPromptEventID(runID string) string {
	return stableRunEventID(runID, runEventIdentityInitialPrompt, nil)
}

func attachedHumanEventID(runID, clientFrameID string, index uint64, message string) string {
	if frameID := strings.TrimSpace(clientFrameID); frameID != "" {
		return stableRunEventID(runID, runEventIdentityAttachedHuman, identityToken(attachedEventExplicitToken, []byte(frameID)))
	}
	digest := sha256.Sum256([]byte(message))
	return stableRunEventID(runID, runEventIdentityAttachedHuman, identityToken(attachedEventDerivedToken, uint64Bytes(index), digest[:]))
}

func attachedAgentEventID(runID string, sequence uint64, frame []byte) string {
	if sequence != 0 {
		return stableRunEventID(runID, runEventIdentityAttachedAgent, identityToken(attachedEventExplicitToken, uint64Bytes(sequence)))
	}
	digest := sha256.Sum256(frame)
	return stableRunEventID(runID, runEventIdentityAttachedAgent, identityToken(attachedEventDerivedToken, digest[:]))
}

func terminalAgentEventID(runID string) string {
	return stableRunEventID(runID, runEventIdentityTerminalAgent, nil)
}

func terminalStatusEventID(runID string) string {
	return stableRunEventID(runID, runEventIdentityTerminalStatus, nil)
}

func stableRunEventID(runID string, kind runEventIdentityKind, token []byte) string {
	payload := identityToken(runEventIdentityFormatVersion, []byte{byte(kind)}, []byte(runID), token)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func identityToken(version byte, parts ...[]byte) []byte {
	size := 1
	for _, part := range parts {
		size += 4 + len(part)
	}
	payload := make([]byte, 1, size)
	payload[0] = version
	for _, part := range parts {
		length := make([]byte, 4)
		binary.BigEndian.PutUint32(length, uint32(len(part)))
		payload = append(payload, length...)
		payload = append(payload, part...)
	}
	return payload
}

func uint64Bytes(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}
