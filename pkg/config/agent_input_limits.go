package config

import "github.com/chaitin/agent-compose/pkg/agentinput"

// AgentInputLimits returns the daemon-wide turn limits. A nil config or an
// unset value falls back to the agentinput defaults. Upload limits are zero
// when the default driver cannot receive upload-sourced files (K8s pushes
// files through a whole-buffer guest writer, which is unsuitable for large
// staged objects).
func (c *Config) AgentInputLimits() agentinput.Limits {
	limits := agentinput.DefaultLimits()
	if c == nil {
		return limits
	}
	if c.AgentInputFileLimitBytes > 0 {
		limits.MaxFileBytes = uint64(c.AgentInputFileLimitBytes)
	}
	if c.AgentInputTurnLimitBytes > 0 {
		limits.MaxTurnBytes = uint64(c.AgentInputTurnLimitBytes)
	}
	if c.AgentInputFileLimit > 0 {
		limits.MaxFiles = uint32(c.AgentInputFileLimit)
	}
	if c.RuntimeDriver == RuntimeDriverK8s {
		return limits
	}
	if c.AgentInputUploadFileLimitBytes > 0 {
		limits.MaxUploadFileBytes = uint64(c.AgentInputUploadFileLimitBytes)
	}
	if c.AgentInputUploadTurnLimitBytes > 0 {
		limits.MaxUploadTurnBytes = uint64(c.AgentInputUploadTurnLimitBytes)
	}
	return limits
}
