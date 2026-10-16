//go:build k8scompose

package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// k8sAPIServerProbeTimeout bounds the reachability probe a sandbox start makes
// before any runtime state exists. A cluster that cannot answer within it is
// reported as unusable instead of being started against, so the failure is a
// precondition error rather than a Pod that is created and never becomes Ready.
const k8sAPIServerProbeTimeout = 10 * time.Second

// PreflightSandboxRuntime reports whether the cluster a sandbox would run in can
// be used at all, before the start path creates any runtime state.
//
// It exists because an unusable cluster previously surfaced only after the
// sandbox record existed: a missing or unreadable kubeconfig turned a start into
// a failure that was indistinguishable from a Pod that failed to become Ready,
// and the sandbox was left for cleanup against a cluster that was never
// reachable.
//
// The check deliberately does not cache a failure, matching client(): a
// kubeconfig mounted after a first attempt must still be picked up by a later
// start, so this only ever caches the client a successful probe built.
func (r *k8sRuntime) PreflightSandboxRuntime(ctx context.Context, sandbox *Sandbox, vmState VMState) error {
	subject := "this sandbox"
	if sandbox != nil {
		if id := strings.TrimSpace(sandbox.Summary.ID); id != "" {
			subject = fmt.Sprintf("sandbox %s", id)
		}
	}
	clientset, _, err := r.client(vmState.K8sContext)
	if err != nil {
		return domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("kubernetes runtime is not usable for %s: verify K8S_KUBECONFIG/KUBECONFIG", subject), err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, k8sAPIServerProbeTimeout)
	defer cancel()
	// /version needs no RBAC grant and is answered by the API server itself, so a
	// refusal here means "not reachable" rather than "not permitted".
	if _, err := clientset.Discovery().RESTClient().Get().AbsPath("/version").Do(probeCtx).Raw(); err != nil {
		return domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("kubernetes API server is not reachable for %s", subject), err)
	}
	return nil
}
