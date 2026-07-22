//go:build linux && cgo && microsandboxcgo

package driver

import (
	"fmt"
	"os/exec"
	"strings"
)

func validateMicrosandboxDiskTools() error {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		return fmt.Errorf("microsandbox requires qemu-img: %w", err)
	}
	mkfsPath, err := exec.LookPath("mkfs.ext4")
	if err != nil {
		return fmt.Errorf("microsandbox requires mkfs.ext4: %w", err)
	}
	output, _ := exec.Command(mkfsPath).CombinedOutput()
	if !strings.Contains(string(output), "[-d root-directory") {
		return fmt.Errorf("microsandbox requires mkfs.ext4 with -d root-directory support")
	}
	return nil
}
