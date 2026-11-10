package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment variables for the opt-in L7 TLS interception mode. They are
// deployment knobs: project content cannot enable interception for a daemon.
const (
	// EgressL7TLSMITMName enables TLS termination in the egress forward proxy.
	// Without it the proxy keeps its L4 CONNECT behavior, and a declared L7
	// constraint on an HTTPS endpoint fails closed instead of being forwarded
	// uninspected.
	EgressL7TLSMITMName = "EGRESS_L7_TLS_MITM"
	// EgressL7CACertFileEnv and EgressL7CAKeyFileEnv point at the CA certificate
	// and private key the proxy signs per-host leaves with.
	EgressL7CACertFileEnv = "EGRESS_L7_CA_CERT_FILE"
	EgressL7CAKeyFileEnv  = "EGRESS_L7_CA_KEY_FILE"
	// EgressL7CACertPEMEnv and EgressL7CAKeyPEMEnv carry the same material
	// inline, for a deployment that injects it as an environment secret.
	EgressL7CACertPEMEnv = "EGRESS_L7_CA_CERT_PEM"
	EgressL7CAKeyPEMEnv  = "EGRESS_L7_CA_KEY_PEM"
	// EgressL7CABundleGuestPathEnv is the guest path at which the CA bundle is
	// already installed. Setting it makes the engine add SSL_CERT_FILE,
	// CURL_CA_BUNDLE, REQUESTS_CA_BUNDLE, and NODE_EXTRA_CA_CERTS when it
	// creates a sandbox runtime.
	EgressL7CABundleGuestPathEnv = "EGRESS_L7_CA_BUNDLE_GUEST_PATH"
)

// EgressL7TLSConfig is the daemon's opt-in TLS interception configuration. A
// zero value leaves the proxy on its L4 behavior.
//
// The CA material is either a certificate and key file or inline PEM. When
// neither is configured the proxy generates an ephemeral CA at startup: that is
// usable for a test or a short-lived deployment, but every restart changes the
// CA, so a deployed sandbox must be given a stable CA to trust.
type EgressL7TLSConfig struct {
	MITM              bool
	CACertFile        string
	CAKeyFile         string
	CACertPEM         string
	CAKeyPEM          string
	CABundleGuestPath string
}

// ConfiguredCA reports whether the operator supplied a CA instead of relying on
// the ephemeral one.
func (c EgressL7TLSConfig) ConfiguredCA() bool {
	return c.CACertFile != "" || c.CACertPEM != ""
}

// loadEgressL7TLSConfig reads the opt-in mode and its CA configuration. It
// rejects a partial or contradictory configuration at startup rather than
// silently disabling interception or replacing an operator CA with an ephemeral
// one.
func loadEgressL7TLSConfig() (EgressL7TLSConfig, error) {
	var config EgressL7TLSConfig
	if raw := strings.TrimSpace(os.Getenv(EgressL7TLSMITMName)); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return EgressL7TLSConfig{}, fmt.Errorf("%s must be true or false: %w", EgressL7TLSMITMName, err)
		}
		config.MITM = enabled
	}
	config.CACertFile = strings.TrimSpace(os.Getenv(EgressL7CACertFileEnv))
	config.CAKeyFile = strings.TrimSpace(os.Getenv(EgressL7CAKeyFileEnv))
	config.CACertPEM = strings.TrimSpace(os.Getenv(EgressL7CACertPEMEnv))
	config.CAKeyPEM = strings.TrimSpace(os.Getenv(EgressL7CAKeyPEMEnv))
	config.CABundleGuestPath = strings.TrimSpace(os.Getenv(EgressL7CABundleGuestPathEnv))

	if (config.CACertFile == "") != (config.CAKeyFile == "") {
		return EgressL7TLSConfig{}, fmt.Errorf("%s and %s must be set together", EgressL7CACertFileEnv, EgressL7CAKeyFileEnv)
	}
	if (config.CACertPEM == "") != (config.CAKeyPEM == "") {
		return EgressL7TLSConfig{}, fmt.Errorf("%s and %s must be set together", EgressL7CACertPEMEnv, EgressL7CAKeyPEMEnv)
	}
	if config.CACertFile != "" && config.CACertPEM != "" {
		return EgressL7TLSConfig{}, fmt.Errorf("%s and %s are alternatives; set only one CA source", EgressL7CACertFileEnv, EgressL7CACertPEMEnv)
	}
	for name, path := range map[string]string{
		EgressL7CACertFileEnv: config.CACertFile,
		EgressL7CAKeyFileEnv:  config.CAKeyFile,
	} {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return EgressL7TLSConfig{}, fmt.Errorf("%s must be an absolute path, got %q", name, path)
		}
	}
	if config.CABundleGuestPath != "" {
		if !config.MITM {
			return EgressL7TLSConfig{}, fmt.Errorf("%s has no effect unless %s is true", EgressL7CABundleGuestPathEnv, EgressL7TLSMITMName)
		}
		if !filepath.IsAbs(config.CABundleGuestPath) {
			return EgressL7TLSConfig{}, fmt.Errorf("%s must be an absolute guest path, got %q", EgressL7CABundleGuestPathEnv, config.CABundleGuestPath)
		}
	}
	if !config.MITM && (config.CACertFile != "" || config.CACertPEM != "") {
		// A configured CA without interception is dead configuration and would
		// mislead an operator into believing TLS is inspected.
		return EgressL7TLSConfig{}, fmt.Errorf("%s is false, so the configured L7 CA is unused; set %s=true or remove the CA configuration", EgressL7TLSMITMName, EgressL7TLSMITMName)
	}
	return config, nil
}
