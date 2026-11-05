package app

import (
	"fmt"
	"os"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// loadEgressL7CA returns the CA material the egress proxy signs leaves with. A
// nil result with no error asks the proxy to generate an ephemeral CA, which is
// what an opt-in deployment without a configured CA gets.
//
// Reading the files here keeps the proxy constructor free of filesystem
// side effects: it receives bytes that the composition boundary loaded, so the
// proxy can be built from a test without a temporary file.
func loadEgressL7CA(config *appconfig.Config) (certPEM, keyPEM []byte, err error) {
	if config == nil || !config.EgressL7TLS.MITM {
		return nil, nil, nil
	}
	l7 := config.EgressL7TLS
	if l7.CACertPEM != "" {
		return []byte(l7.CACertPEM), []byte(l7.CAKeyPEM), nil
	}
	if l7.CACertFile == "" {
		return nil, nil, nil
	}
	certPEM, err = os.ReadFile(l7.CACertFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s %s: %w", appconfig.EgressL7CACertFileEnv, l7.CACertFile, err)
	}
	keyPEM, err = os.ReadFile(l7.CAKeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s %s: %w", appconfig.EgressL7CAKeyFileEnv, l7.CAKeyFile, err)
	}
	return certPEM, keyPEM, nil
}
