//go:build !linux

package driver

import (
	"context"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/nfsvolume"
)

func mountNFSVolume(context.Context, nfsvolume.Source, string, bool) error {
	return fmt.Errorf("nfs volumes on a VM runtime require a Linux host")
}

func nfsMountState(string) (bool, bool, error) {
	return false, false, nil
}
