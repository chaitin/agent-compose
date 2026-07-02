//go:build cgo

package driver

import (
	"testing"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func TestMicrosandboxPullPolicyForImageRef(t *testing.T) {
	if got := microsandboxPullPolicyForImageRef("/cache/rootfs"); got != microsandbox.PullPolicyNever {
		t.Fatalf("absolute rootfs pull policy = %v, want Never", got)
	}
	if got := microsandboxPullPolicyForImageRef("guest:latest"); got != microsandbox.PullPolicyIfMissing {
		t.Fatalf("image ref pull policy = %v, want IfMissing", got)
	}
}
