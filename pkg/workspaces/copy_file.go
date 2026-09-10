package workspaces

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// copyWorkspaceFile never hardlinks writable content. Filesystems that support
// cloning share blocks with copy-on-write isolation; others receive a byte copy.
func copyWorkspaceFile(ctx context.Context, source *os.File, destination string, mode os.FileMode, clone func(*os.File, string) error) (retErr error) {
	defer func() {
		if retErr != nil {
			if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				retErr = errors.Join(retErr, err)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("workspace copy source must be a regular file")
	}
	if err := clone(source, destination); err == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return os.Chmod(destination, mode)
	} else if !cloneUnsupported(err) {
		return fmt.Errorf("clone workspace file: %w", err)
	}
	// A failed ioctl can leave an empty or partially cloned destination.
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, contextCopyReader{ctx: ctx, source: source})
	if copyErr == nil {
		copyErr = target.Chmod(mode)
	}
	return errors.Join(copyErr, target.Close())
}

type contextCopyReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextCopyReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
