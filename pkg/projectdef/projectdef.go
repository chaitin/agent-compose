// Package projectdef exposes the stable, definition-only project API.
//
// It intentionally contains no persistence or runtime-driver concerns. The
// aliases keep the public surface compatible with the compose schema while
// allowing callers to depend on a package whose contract is project
// definition parsing, normalization, and canonical serialization.
package projectdef

import "github.com/chaitin/agent-compose/pkg/compose"

type ProjectSpec = compose.ProjectSpec
type AgentSpec = compose.AgentSpec
type AgentMCPEntriesSpec = compose.AgentMCPEntriesSpec
type AgentMCPEntrySpec = compose.AgentMCPEntrySpec
type BuildSpec = compose.BuildSpec
type DriverSpec = compose.DriverSpec
type DockerDriverSpec = compose.DockerDriverSpec
type BoxliteDriverSpec = compose.BoxliteDriverSpec
type MicrosandboxDriverSpec = compose.MicrosandboxDriverSpec
type FirecrackerDriverSpec = compose.FirecrackerDriverSpec
type K8sDriverSpec = compose.K8sDriverSpec
type EnvFileSpec = compose.EnvFileSpec
type EnvVarSpec = compose.EnvVarSpec
type EventTriggerSpec = compose.EventTriggerSpec
type JupyterSpec = compose.JupyterSpec
type MCPServerSpec = compose.MCPServerSpec
type OctoBusServerSpec = compose.OctoBusServerSpec
type SandboxSpec = compose.SandboxSpec
type SchedulerSpec = compose.SchedulerSpec
type SkillSpec = compose.SkillSpec
type TriggerSpec = compose.TriggerSpec
type VolumeMountSpec = compose.VolumeMountSpec
type VolumeSpec = compose.VolumeSpec
type WorkspaceSpec = compose.WorkspaceSpec
type ScriptSource = compose.ScriptSource
type ScriptSourceResolver = compose.ScriptSourceResolver
type ScriptSourceResolverFunc = compose.ScriptSourceResolverFunc
type NormalizeOptions = compose.NormalizeOptions
type NormalizedAgentSpec = compose.NormalizedAgentSpec
type NormalizedBuildSpec = compose.NormalizedBuildSpec
type NormalizedDriverSpec = compose.NormalizedDriverSpec
type NormalizedMCPServerSpec = compose.NormalizedMCPServerSpec
type NormalizedOctoBusServerSpec = compose.NormalizedOctoBusServerSpec
type NormalizedProjectSpec = compose.NormalizedProjectSpec
type NormalizedSandboxSpec = compose.NormalizedSandboxSpec
type NormalizedSchedulerSpec = compose.NormalizedSchedulerSpec
type NormalizedSkillSpec = compose.NormalizedSkillSpec
type NormalizedTriggerSpec = compose.NormalizedTriggerSpec
type NormalizedVolumeMountSpec = compose.NormalizedVolumeMountSpec
type NormalizedVolumeSpec = compose.NormalizedVolumeSpec
type ValidationError = compose.ValidationError

// Parse decodes a project definition from YAML or JSON.
func Parse(data []byte) (*ProjectSpec, error) { return compose.Parse(data) }

// ParseFile decodes a project definition from a file.
func ParseFile(path string) (*ProjectSpec, error) { return compose.ParseFile(path) }

// Normalize applies project defaults and validates definition-level rules.
func Normalize(spec *ProjectSpec, options NormalizeOptions) (*NormalizedProjectSpec, error) {
	return compose.Normalize(spec, options)
}

// NormalizeFile loads and normalizes a project definition file.
func NormalizeFile(path string) (*NormalizedProjectSpec, error) {
	return compose.NormalizeFile(path)
}

// Validate performs definition-level validation without runtime services.
func Validate(spec *ProjectSpec, options NormalizeOptions) error {
	// Validation must not resolve credentials or fetch script sources. Those
	// operations belong to the agent-compose runtime validation layer.
	options.SourceCredentials = compose.SourceCredentialsFromReferences
	options.ResolveScriptURLs = false
	options.ScriptSourceResolver = nil
	_, err := compose.Normalize(spec, options)
	return err
}

// ParseCanonicalJSON decodes the canonical normalized representation.
func ParseCanonicalJSON(data []byte) (*NormalizedProjectSpec, error) {
	return compose.ParseCanonicalJSON(data)
}

// IsProjectName reports whether value follows the project naming contract.
func IsProjectName(value string) bool { return compose.IsProjectName(value) }

const ProjectNamePattern = compose.ProjectNamePattern

const (
	SourceCredentialsFromReferences = compose.SourceCredentialsFromReferences
	SourceCredentialsResolved       = compose.SourceCredentialsResolved

	ScriptSourceBoundaryAuthoring = compose.ScriptSourceBoundaryAuthoring
	ScriptSourceBoundaryDaemon    = compose.ScriptSourceBoundaryDaemon
)
