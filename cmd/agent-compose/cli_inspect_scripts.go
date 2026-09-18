package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

type composeOmittedScript struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func omitInspectScripts(output any) (any, error) {
	switch value := output.(type) {
	case composeProjectOutput:
		config, omitted, err := omitDeclaredScripts(value.DeclaredConfig)
		value.DeclaredConfig, value.OmittedScripts = config, omitted
		return value, err
	case composeAgentInspectOutput:
		config, omitted, err := omitDeclaredScripts(value.DeclaredConfig)
		value.DeclaredConfig, value.OmittedScripts = config, omitted
		return value, err
	default:
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("--omit-scripts requires a project or agent resource")}
	}
}

// Decode only the declaration so script-like text in run output or other
// configuration fields cannot be mistaken for a scheduler script body.
func omitDeclaredScripts(raw json.RawMessage) (json.RawMessage, []composeOmittedScript, error) {
	if len(raw) == 0 {
		return raw, nil, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, nil, fmt.Errorf("decode inspect configuration: %w", err)
	}
	if config == nil {
		return raw, nil, nil
	}
	var omitted []composeOmittedScript
	if agentsJSON, ok := config["agents"]; ok {
		var agents []map[string]json.RawMessage
		if err := json.Unmarshal(agentsJSON, &agents); err != nil {
			return nil, nil, err
		}
		for i, agent := range agents {
			item, err := omitAgentScript(agent, fmt.Sprintf("declared_config.agents[%d].scheduler.script", i))
			if err != nil {
				return nil, nil, err
			}
			if item != nil {
				omitted = append(omitted, *item)
			}
		}
		data, err := json.Marshal(agents)
		if err != nil {
			return nil, nil, err
		}
		config["agents"] = data
	} else {
		item, err := omitAgentScript(config, "declared_config.scheduler.script")
		if err != nil {
			return nil, nil, err
		}
		if item != nil {
			omitted = append(omitted, *item)
		}
	}
	data, err := json.Marshal(config)
	return data, omitted, err
}

func omitAgentScript(agent map[string]json.RawMessage, path string) (*composeOmittedScript, error) {
	raw, ok := agent["scheduler"]
	if !ok {
		return nil, nil
	}
	var scheduler map[string]json.RawMessage
	if err := json.Unmarshal(raw, &scheduler); err != nil {
		return nil, err
	}
	scriptJSON, ok := scheduler["script"]
	if !ok {
		return nil, nil
	}
	var script string
	if err := json.Unmarshal(scriptJSON, &script); err != nil {
		return nil, err
	}
	if script == "" {
		return nil, nil
	}
	delete(scheduler, "script")
	data, err := json.Marshal(scheduler)
	if err != nil {
		return nil, err
	}
	agent["scheduler"] = data
	return &composeOmittedScript{Path: path, Bytes: len(script), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(script)))}, nil
}
