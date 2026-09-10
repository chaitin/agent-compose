//go:build !linux && !darwin

package execution

import "fmt"

func exchangeAgentSkillPaths(_, _ string) error {
	return fmt.Errorf("atomic skills directory exchange is unavailable on this platform")
}
