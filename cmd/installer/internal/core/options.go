package core

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultInstallDir = "/opt/agent-compose"
	DefaultRepository = "chaitin/agent-compose"
	DefaultVersion    = "latest"
	DefaultPort       = 80
)

type Operation string

const (
	OperationInstall   Operation = "install"
	OperationUpgrade   Operation = "upgrade"
	OperationUninstall Operation = "uninstall"
)

type Options struct {
	InstallDir      string
	Repository      string
	ReleaseBaseURL  string
	Version         string
	ImagePrefix     string
	FrontendVersion string
	Port            int
	PortSet         bool
	WithUI          bool
	WithUISet       bool
	NoStart         bool
	Purge           bool
	KVMPath         string
	BundleDir       string
	InstallerPath   string
}

func DefaultOptions() Options {
	return Options{
		InstallDir:      DefaultInstallDir,
		Repository:      DefaultRepository,
		Version:         DefaultVersion,
		FrontendVersion: DefaultVersion,
		Port:            DefaultPort,
		KVMPath:         "/dev/kvm",
	}
}

func (o Options) Validate(operation Operation) error {
	if operation != OperationInstall && operation != OperationUpgrade && operation != OperationUninstall {
		return fmt.Errorf("unsupported installer operation %q", operation)
	}
	if !filepath.IsAbs(o.InstallDir) {
		return fmt.Errorf("install directory must be absolute: %s", o.InstallDir)
	}
	if strings.TrimSpace(o.Repository) == "" || strings.ContainsAny(o.Repository, "\r\n") {
		return fmt.Errorf("repository must be a non-empty owner/name")
	}
	if operation != OperationUninstall {
		if strings.TrimSpace(o.Version) == "" || strings.ContainsAny(o.Version, "\r\n") {
			return fmt.Errorf("version must not be empty")
		}
		if o.Port < 1 || o.Port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
	}
	return nil
}

func ParsePort(value string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("port must be between 1 and 65535")
	}
	return port, nil
}
