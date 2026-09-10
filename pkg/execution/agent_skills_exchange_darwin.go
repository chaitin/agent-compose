//go:build darwin

package execution

import "golang.org/x/sys/unix"

func exchangeAgentSkillPaths(left, right string) error {
	return unix.RenamexNp(left, right, unix.RENAME_SWAP)
}
