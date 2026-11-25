package driver

import (
	"context"
	"fmt"
	"sync"
)

// A kernel NFS mount cannot be interrupted, and against an unreachable server
// it can block for minutes. A caller must still be able to give up when its
// context ends, so the mount runs in an attempt that outlives the caller and
// is tracked per mount point:
//
//   - A caller that finds an attempt in flight waits for it instead of
//     issuing a second mount on the same directory.
//   - When an attempt succeeds after every caller waiting on it has given
//     up, nobody will use the mount, so the attempt unmounts it.
//   - Inspecting the mount point, starting an attempt and that unmount all
//     happen under one lock, so a caller never sees a mount that an
//     abandoned attempt is about to remove.
type nfsMountAttempt struct {
	done    chan struct{}
	err     error
	waiters int
}

var nfsMountAttempts = struct {
	sync.Mutex
	byPoint map[string]*nfsMountAttempt
}{byPoint: make(map[string]*nfsMountAttempt)}

// ensureNFSMountPoint makes the export mounted on item.mountPoint. It reports
// whether this call's attempt mounted it, so the caller can roll back on a
// later failure; a mount that already existed is left alone.
func ensureNFSMountPoint(ctx context.Context, item sandboxNFSMount) (bool, error) {
	nfsMountAttempts.Lock()
	attempt, inFlight := nfsMountAttempts.byPoint[item.mountPoint]
	if inFlight {
		attempt.waiters++
	} else {
		isMount, isNFS, err := nfsVolumeMountStateOf(item.mountPoint)
		if err != nil {
			nfsMountAttempts.Unlock()
			return false, fmt.Errorf("inspect nfs volume mount point %s: %w", item.mountPoint, err)
		}
		if isMount {
			nfsMountAttempts.Unlock()
			if isNFS {
				return false, nil
			}
			return false, fmt.Errorf("nfs volume mount point %s already has a non-NFS mount", item.mountPoint)
		}
		attempt = &nfsMountAttempt{done: make(chan struct{}), waiters: 1}
		nfsMountAttempts.byPoint[item.mountPoint] = attempt
		go runNFSMountAttempt(context.WithoutCancel(ctx), item, attempt)
	}
	nfsMountAttempts.Unlock()

	select {
	case <-attempt.done:
	case <-ctx.Done():
		nfsMountAttempts.Lock()
		select {
		case <-attempt.done:
			// Finished while the lock was being taken: the result is ours.
		default:
			attempt.waiters--
			nfsMountAttempts.Unlock()
			return false, ctx.Err()
		}
		nfsMountAttempts.Unlock()
	}
	if attempt.err != nil {
		return false, fmt.Errorf("mount nfs volume %q (%s): %w", item.mount.Source, item.source.Device(), attempt.err)
	}
	return true, nil
}

func runNFSMountAttempt(ctx context.Context, item sandboxNFSMount, attempt *nfsMountAttempt) {
	err := nfsVolumeMounter(ctx, item.source, item.mountPoint, item.mount.ReadOnly)
	nfsMountAttempts.Lock()
	defer nfsMountAttempts.Unlock()
	if err == nil && attempt.waiters == 0 {
		if unmountErr := boxliteVolumeBridgeUnmounter(item.mountPoint); unmountErr != nil {
			err = fmt.Errorf("release abandoned nfs mount %s: %w", item.mountPoint, unmountErr)
		} else {
			err = context.Canceled
		}
	}
	attempt.err = err
	delete(nfsMountAttempts.byPoint, item.mountPoint)
	close(attempt.done)
}
