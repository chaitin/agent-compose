package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/egress"
	"gopkg.in/yaml.v3"
)

// EgressGlobalPolicyFileEnv names an optional YAML file that declares the
// daemon-wide outbound network policy. It has the same body as a compose
// policy.network block: default, allow, and deny.
const EgressGlobalPolicyFileEnv = "EGRESS_GLOBAL_POLICY_FILE"

// GlobalNetworkPolicyInfo identifies the daemon-wide policy that was loaded at
// startup. It is the audit handle an operator needs when the effective policy
// changes between deployments: the path names the input and the digest pins the
// exact bytes that were compiled.
type GlobalNetworkPolicyInfo struct {
	// Path is the configured file path, empty when no global policy is set.
	Path string
	// SHA256 is the lowercase hex digest of the file bytes, empty when no global
	// policy is set.
	SHA256 string
}

// loadGlobalNetworkPolicy reads the daemon-wide policy once at startup. An
// unset variable means no daemon policy and preserves the undeclared behavior
// exactly. A configured file is parsed with unknown fields rejected so a typo
// cannot silently broaden the policy. It must also state its default
// explicitly: unlike an agent block, a daemon-wide file is the security
// boundary, and inheriting the compose "omitted means allow-all" rule would
// turn a file that declares only allowances into an unrestricted policy. After
// that check it goes through the same compose normalization every agent
// declaration uses.
//
// It also returns the file identity so admission reports can say which daemon
// policy they were computed against. The digest covers the bytes that were
// actually parsed, not a later read of the path, so it cannot drift from the
// compiled policy.
func loadGlobalNetworkPolicy() (*egress.NetworkDeclaration, GlobalNetworkPolicyInfo, error) {
	path := strings.TrimSpace(os.Getenv(EgressGlobalPolicyFileEnv))
	if path == "" {
		return nil, GlobalNetworkPolicyInfo{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, GlobalNetworkPolicyInfo{}, fmt.Errorf("read %s %q: %w", EgressGlobalPolicyFileEnv, path, err)
	}
	digest := sha256.Sum256(data)
	info := GlobalNetworkPolicyInfo{Path: path, SHA256: hex.EncodeToString(digest[:])}
	if legacyProtocolPresent(data) {
		return nil, info, fmt.Errorf("validate %s %q: protocol was removed from network entries; remove it, entries match any protocol", EgressGlobalPolicyFileEnv, path)
	}
	var spec compose.SandboxNetworkSpec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&spec); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, info, fmt.Errorf("%s %q is empty", EgressGlobalPolicyFileEnv, path)
		}
		return nil, info, fmt.Errorf("parse %s %q: %w", EgressGlobalPolicyFileEnv, path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, info, fmt.Errorf("%s %q must contain exactly one YAML document", EgressGlobalPolicyFileEnv, path)
		}
		return nil, info, fmt.Errorf("parse %s %q: %w", EgressGlobalPolicyFileEnv, path, err)
	}
	if strings.TrimSpace(spec.Default) == "" {
		return nil, info, fmt.Errorf("validate %s %q: default is required; set it to %q or %q so the daemon boundary is explicit", EgressGlobalPolicyFileEnv, path, compose.SandboxNetworkDefaultAllowAll, compose.SandboxNetworkDefaultDenyAll)
	}
	normalized, err := compose.NormalizeSandboxNetworkSpec("daemon network policy", &spec)
	if err != nil {
		return nil, info, fmt.Errorf("validate %s %q: %w", EgressGlobalPolicyFileEnv, path, err)
	}
	declaration, err := normalized.EgressDeclaration()
	if err != nil {
		return nil, info, fmt.Errorf("compile %s %q: %w", EgressGlobalPolicyFileEnv, path, err)
	}
	return &declaration, info, nil
}

// legacyProtocolPresent detects the removed protocol field before strict
// decoding. The strict decoder already rejects it, but its type-level error does
// not tell an operator what to replace; this check keeps the daemon-policy file
// error consistent with the compose parser's migration message.
func legacyProtocolPresent(data []byte) bool {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return false
	}
	return yamlMappingHasKey(&document, "protocol")
}

func yamlMappingHasKey(node *yaml.Node, key string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return true
			}
			if yamlMappingHasKey(node.Content[i+1], key) {
				return true
			}
		}
		return false
	}
	for _, child := range node.Content {
		if yamlMappingHasKey(child, key) {
			return true
		}
	}
	return false
}
