// Package nfsvolume describes an NFS export that backs an agent-compose
// volume. It is shared by the volume manager, which validates and stores the
// export, and the runtime drivers, which mount it with their own mechanism:
// the Docker Engine's local volume driver, a kernel mount made by the process
// that runs a VM, or an inline Kubernetes NFS volume.
//
// The resolved export travels to the runtimes as a reference string in
// SandboxVolumeMount.HostPath (see Ref and ParseRef), the same field the k8s
// volume driver uses for its PVC reference, so neither the sandbox record nor
// the sandbox-host protocol needs another field.
package nfsvolume

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

// DriverName is the volume driver name stored in volume records and resolved
// sandbox mounts.
const DriverName = "nfs"

// Volume option keys accepted by the nfs driver.
const (
	OptionServer       = "server"
	OptionExport       = "export"
	OptionVersion      = "version"
	OptionMountOptions = "mount_options"
)

// DefaultVersion is the NFS protocol version used when a volume does not pin
// one. Every runtime that mounts through agent-compose uses the same version,
// so the same volume behaves the same way on Docker and on a VM runtime.
const DefaultVersion = "4"

const refScheme = "nfs"

// DockerVolumeNamePrefix starts the name of every Docker volume agent-compose
// creates for an nfs export.
const DockerVolumeNamePrefix = "agent-compose-nfs-"

var (
	supportedVersions = []string{"3", "4", "4.0", "4.1", "4.2"}
	hostnamePattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	mountOptionToken  = regexp.MustCompile(`^[a-z][a-z0-9_]*(=[A-Za-z0-9_.:/+-]+)?$`)
	exportPattern     = regexp.MustCompile(`^/[A-Za-z0-9_.@+/-]*$`)
	// Options agent-compose sets itself. The server address and version come
	// from their own fields, read-only is a property of each agent's mount,
	// not of the volume, and every runtime mounts with nosuid,nodev, so a
	// volume cannot turn setuid files or device nodes on the server back on.
	reservedMountOptions = []string{"addr", "vers", "nfsvers", "ro", "rw", "suid", "nosuid", "dev", "nodev"}
)

// Source is one validated NFS export.
type Source struct {
	Server string
	Export string
	// Version is empty when the volume did not pin one; EffectiveVersion then
	// supplies DefaultVersion.
	Version      string
	MountOptions []string
}

// FromOptions validates the options of an nfs volume and returns the export
// together with the canonical options to store on the volume record.
func FromOptions(options map[string]string) (Source, map[string]string, error) {
	for key := range options {
		switch key {
		case OptionServer, OptionExport, OptionVersion, OptionMountOptions:
		default:
			return Source{}, nil, fmt.Errorf("nfs volume option %q is not supported; use server, export, version or mount_options", key)
		}
	}
	source := Source{
		Server:  strings.TrimSpace(options[OptionServer]),
		Export:  strings.TrimSpace(options[OptionExport]),
		Version: strings.TrimSpace(options[OptionVersion]),
	}
	if raw := strings.TrimSpace(options[OptionMountOptions]); raw != "" {
		for _, item := range strings.Split(raw, ",") {
			source.MountOptions = append(source.MountOptions, strings.TrimSpace(item))
		}
	}
	source, err := source.normalize()
	if err != nil {
		return Source{}, nil, err
	}
	return source, source.Options(), nil
}

// Options returns the canonical volume options for the export.
func (s Source) Options() map[string]string {
	out := map[string]string{OptionServer: s.Server, OptionExport: s.Export}
	if s.Version != "" {
		out[OptionVersion] = s.Version
	}
	if len(s.MountOptions) > 0 {
		out[OptionMountOptions] = strings.Join(s.MountOptions, ",")
	}
	return out
}

func (s Source) normalize() (Source, error) {
	server := strings.TrimSuffix(strings.TrimPrefix(s.Server, "["), "]")
	if server == "" {
		return Source{}, fmt.Errorf("nfs volume option server is required")
	}
	if ip := net.ParseIP(server); ip != nil {
		server = ip.String()
	} else if !hostnamePattern.MatchString(server) || len(server) > 253 {
		return Source{}, fmt.Errorf("nfs volume server %q must be a hostname or IP address without a port; set a port with mount_options port=<n>", s.Server)
	}
	s.Server = server
	if s.Export == "" {
		return Source{}, fmt.Errorf("nfs volume option export is required")
	}
	if !exportPattern.MatchString(s.Export) || path.Clean(s.Export) != s.Export {
		return Source{}, fmt.Errorf("nfs volume export %q must be a clean absolute path of letters, digits and _.@+-/", s.Export)
	}
	if s.Version != "" && !slices.Contains(supportedVersions, s.Version) {
		return Source{}, fmt.Errorf("nfs volume version %q is not supported; use one of %s", s.Version, strings.Join(supportedVersions, ", "))
	}
	seen := make(map[string]struct{}, len(s.MountOptions))
	for _, item := range s.MountOptions {
		if !mountOptionToken.MatchString(item) {
			return Source{}, fmt.Errorf("nfs volume mount option %q must look like name or name=value", item)
		}
		name, _, _ := strings.Cut(item, "=")
		if slices.Contains(reservedMountOptions, name) {
			return Source{}, fmt.Errorf("nfs volume mount option %q is managed by agent-compose; use the server and version options, and read_only on the agent mount", name)
		}
		if _, ok := seen[name]; ok {
			return Source{}, fmt.Errorf("nfs volume mount option %q is repeated", name)
		}
		seen[name] = struct{}{}
	}
	return s, nil
}

// EffectiveVersion is the NFS version the runtimes mount with.
func (s Source) EffectiveVersion() string {
	if s.Version != "" {
		return s.Version
	}
	return DefaultVersion
}

// HasCustomMountSettings reports whether the volume pins a version or adds
// mount options. An inline Kubernetes NFS volume cannot carry either.
func (s Source) HasCustomMountSettings() bool {
	return s.Version != "" || len(s.MountOptions) > 0
}

// Ref encodes the export as the reference string stored in
// SandboxVolumeMount.HostPath.
func (s Source) Ref() string {
	query := url.Values{}
	if s.Version != "" {
		query.Set(OptionVersion, s.Version)
	}
	if len(s.MountOptions) > 0 {
		query.Set(OptionMountOptions, strings.Join(s.MountOptions, ","))
	}
	ref := url.URL{Scheme: refScheme, Host: hostForURL(s.Server), Path: s.Export, RawQuery: query.Encode()}
	return ref.String()
}

// IsRef reports whether value is an NFS reference rather than a host path.
func IsRef(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), refScheme+"://")
}

// ParseRef decodes a reference produced by Ref and validates it again, so a
// runtime never mounts an export that bypassed volume validation.
func ParseRef(value string) (Source, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != refScheme || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" {
		return Source{}, fmt.Errorf("nfs volume source %q is not an nfs:// reference", value)
	}
	if parsed.Port() != "" {
		return Source{}, fmt.Errorf("nfs volume source %q must not carry a port", value)
	}
	options := map[string]string{OptionServer: parsed.Hostname(), OptionExport: parsed.Path}
	for key, values := range parsed.Query() {
		if len(values) != 1 {
			return Source{}, fmt.Errorf("nfs volume source %q repeats %s", value, key)
		}
		if key != OptionVersion && key != OptionMountOptions {
			return Source{}, fmt.Errorf("nfs volume source %q has unsupported parameter %s", value, key)
		}
		options[key] = values[0]
	}
	source, _, err := FromOptions(options)
	return source, err
}

// Device is the server:/export string a mount names as its source.
func (s Source) Device() string {
	return hostForURL(s.Server) + ":" + s.Export
}

// KernelMountData is the option string for a kernel nfs mount. The kernel
// does not resolve hostnames, so addr must already be an IP address.
func (s Source) KernelMountData(addr string) string {
	parts := []string{"addr=" + addr, "vers=" + s.EffectiveVersion()}
	return strings.Join(append(parts, s.MountOptions...), ",")
}

// DockerVolumeOptions are the Docker local volume driver options that make
// the Engine mount this export. The Engine resolves a hostname in addr itself
// and turns nosuid and nodev into mount flags, matching the VM runtimes.
func (s Source) DockerVolumeOptions() map[string]string {
	parts := []string{"addr=" + s.Server, "vers=" + s.EffectiveVersion()}
	parts = append(append(parts, s.MountOptions...), "nosuid", "nodev")
	return map[string]string{
		"type":   "nfs",
		"o":      strings.Join(parts, ","),
		"device": ":" + s.Export,
	}
}

// DockerVolumeName is the Docker volume that carries this export. It is
// derived from every mount setting, so two volumes share a Docker volume only
// when the Engine would mount them identically, and a changed setting never
// reuses a Docker volume created with the old one.
func (s Source) DockerVolumeName() string {
	sum := sha256.Sum256([]byte(s.Ref()))
	return DockerVolumeNamePrefix + hex.EncodeToString(sum[:])[:24]
}

func hostForURL(server string) string {
	if strings.Contains(server, ":") {
		return "[" + server + "]"
	}
	return server
}
