//go:build cgo

package driver

import (
	"testing"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func TestMicrosandboxPullPolicyForImageRef(t *testing.T) {
	// absolute path is always Never regardless of perCallPolicy
	if got := microsandboxPullPolicyForImageRef("/cache/rootfs", ""); got != microsandbox.PullPolicyNever {
		t.Fatalf("absolute rootfs pull policy (empty) = %v, want Never", got)
	}
	if got := microsandboxPullPolicyForImageRef("/cache/rootfs", "always"); got != microsandbox.PullPolicyNever {
		t.Fatalf("absolute rootfs pull policy (always) = %v, want Never", got)
	}
	// non-absolute with empty perCallPolicy: default behavior = IfMissing (unchanged)
	if got := microsandboxPullPolicyForImageRef("guest:latest", ""); got != microsandbox.PullPolicyIfMissing {
		t.Fatalf("image ref pull policy (empty) = %v, want IfMissing", got)
	}
	// non-absolute with "missing": same as default
	if got := microsandboxPullPolicyForImageRef("guest:latest", "missing"); got != microsandbox.PullPolicyIfMissing {
		t.Fatalf("image ref pull policy (missing) = %v, want IfMissing", got)
	}
	// non-absolute with "always"
	if got := microsandboxPullPolicyForImageRef("guest:latest", "always"); got != microsandbox.PullPolicyAlways {
		t.Fatalf("image ref pull policy (always) = %v, want Always", got)
	}
	// non-absolute with "never"
	if got := microsandboxPullPolicyForImageRef("guest:latest", "never"); got != microsandbox.PullPolicyNever {
		t.Fatalf("image ref pull policy (never) = %v, want Never", got)
	}
}
