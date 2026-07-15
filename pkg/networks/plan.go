package networks

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

type Mode string

const (
	ModeSingleBridge Mode = "single-bridge"
	ModeMultiNetwork Mode = "multi-network"
)

const (
	ManagedLabel     = "agent-compose.managed"
	ResourceLabel    = "agent-compose.resource"
	ProjectIDLabel   = "agent-compose.project-id"
	LogicalNameLabel = "agent-compose.network-name"

	ProjectNetworkResource = "project-network"
)

var dockerNamePartPattern = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

type Definition struct {
	Name   string
	Driver string
}

type Intent struct {
	ProjectID   string
	AgentName   string
	SandboxID   string
	Definitions map[string]Definition
	Attachments []string
}

type Plan struct {
	Mode               Mode
	BaselineNetwork    string
	RequiresDaemonHost bool
	Attachments        []Attachment
}

type Attachment struct {
	LogicalName     string
	RuntimeName     string
	Driver          string
	Aliases         []string
	GatewayPriority int
	Managed         bool
	Labels          map[string]string
}

func BuildPlan(intent Intent, baselineNetwork string) (Plan, error) {
	baselineNetwork = strings.TrimSpace(baselineNetwork)
	if len(intent.Attachments) == 0 {
		if baselineNetwork == "" {
			return Plan{}, fmt.Errorf("baseline docker network is required for single-bridge mode")
		}
		return Plan{
			Mode:            ModeSingleBridge,
			BaselineNetwork: baselineNetwork,
			Attachments: []Attachment{{
				LogicalName:     "default",
				RuntimeName:     baselineNetwork,
				Driver:          "bridge",
				GatewayPriority: 100,
			}},
		}, nil
	}
	projectID := strings.TrimSpace(intent.ProjectID)
	if projectID == "" {
		return Plan{}, fmt.Errorf("project id is required for multi-network mode")
	}
	seen := make(map[string]struct{}, len(intent.Attachments))
	attachments := make([]Attachment, 0, len(intent.Attachments))
	for index, logicalName := range intent.Attachments {
		logicalName = strings.TrimSpace(logicalName)
		if logicalName == "" {
			return Plan{}, fmt.Errorf("network attachment %d is empty", index)
		}
		if _, exists := seen[logicalName]; exists {
			return Plan{}, fmt.Errorf("network attachment %q is duplicated", logicalName)
		}
		seen[logicalName] = struct{}{}
		definition, exists := intent.Definitions[logicalName]
		if !exists {
			return Plan{}, fmt.Errorf("network attachment %q is not defined", logicalName)
		}
		driver := strings.ToLower(strings.TrimSpace(definition.Driver))
		if driver == "" {
			driver = "bridge"
		}
		if driver != "bridge" {
			return Plan{}, fmt.Errorf("network attachment %q uses unsupported driver %q", logicalName, driver)
		}
		priority := 0
		if index == 0 {
			priority = 100
		}
		attachments = append(attachments, Attachment{
			LogicalName:     logicalName,
			RuntimeName:     RuntimeNetworkName(projectID, logicalName),
			Driver:          driver,
			Aliases:         endpointAliases(intent.AgentName, intent.SandboxID),
			GatewayPriority: priority,
			Managed:         true,
			Labels: map[string]string{
				ManagedLabel:     "true",
				ResourceLabel:    ProjectNetworkResource,
				ProjectIDLabel:   projectID,
				LogicalNameLabel: logicalName,
			},
		})
	}
	return Plan{
		Mode:               ModeMultiNetwork,
		BaselineNetwork:    baselineNetwork,
		RequiresDaemonHost: true,
		Attachments:        attachments,
	}, nil
}

func RuntimeNetworkName(projectID, logicalName string) string {
	projectPart := sanitizeNamePart(projectID)
	logicalPart := sanitizeNamePart(logicalName)
	name := "agent-compose-p-" + projectPart + "-" + logicalPart
	if len(name) <= 63 {
		return name
	}
	digest := sha256.Sum256([]byte(projectID + "\x00" + logicalName))
	suffix := fmt.Sprintf("%x", digest[:6])
	if len(projectPart) > 12 {
		projectPart = projectPart[:12]
	}
	readable := "agent-compose-p-" + projectPart + "-" + logicalPart
	maxReadableLength := 63 - 1 - len(suffix)
	if len(readable) > maxReadableLength {
		readable = strings.TrimRight(readable[:maxReadableLength], "-._")
	}
	return readable + "-" + suffix
}

func endpointAliases(agentName, sandboxID string) []string {
	agentName = sanitizeNamePart(agentName)
	sandboxID = sanitizeNamePart(sandboxID)
	aliases := make([]string, 0, 2)
	if agentName != "" {
		aliases = append(aliases, agentName)
	}
	if agentName != "" && sandboxID != "" {
		shortID := sandboxID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		aliases = append(aliases, agentName+"-"+shortID)
	}
	return aliases
}

func sanitizeNamePart(value string) string {
	value = strings.Trim(dockerNamePartPattern.ReplaceAllString(strings.TrimSpace(value), "-"), "-._")
	return value
}
