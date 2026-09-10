//go:build !linux && !darwin

package workspaces

import (
	"errors"
	"os"
)

func cloneWorkspaceFile(*os.File, string) error { return errors.ErrUnsupported }
func cloneUnsupported(err error) bool           { return errors.Is(err, errors.ErrUnsupported) }

func openWorkspaceCopySource(root *os.Root, path string) (*os.File, error) { return root.Open(path) }
