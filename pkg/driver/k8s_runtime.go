//go:build k8scompose

package driver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	validation "k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

const (
	k8sSandboxLabelPrefix = "agent-compose"
	k8sSandboxLabelID     = k8sSandboxLabelPrefix + ".sandbox_id"
	k8sSandboxLabelDriver = k8sSandboxLabelPrefix + ".driver"
	// k8sSandboxLabelJupyter records, on the Pod itself, whether it was
	// created with jupyter as its entrypoint ("true"/"false") - see
	// createPod and k8sPodNeedsRecreate. A missing label (any Pod created
	// before this label existed) is treated as "false", which correctly
	// flags a mismatch - and triggers a recreate - the first time such a
	// Pod is seen with jupyter now enabled for its sandbox.
	k8sSandboxLabelJupyter = k8sSandboxLabelPrefix + ".jupyter"

	// k8sContainerName is the single container agent-compose runs per sandbox
	// Pod. v1 does not support multi-container sandboxes.
	k8sContainerName = "agent"

	k8sPodPollInterval     = 200 * time.Millisecond
	k8sDefaultStartTimeout = 2 * time.Minute

	// k8sPodDeleteTimeout bounds waitForPodDeleted: how long EnsureSandbox
	// waits for a stale Pod's name to actually free up after a
	// force-delete, before giving up on recreating it this call.
	k8sPodDeleteTimeout = 30 * time.Second

	// k8sCleanupTimeout bounds best-effort cleanup calls (deleting a Pod this
	// same EnsureSandbox call just created, after it failed to become ready)
	// so cleanup can still run against a context that is already canceled or
	// deadline-exceeded, mirroring the docker driver's
	// dockerStopFallbackActionTimeout.
	k8sCleanupTimeout = 5 * time.Second
)

// k8sStuckWaitingReasons are container Waiting reasons Kubernetes will keep
// retrying forever but that, in practice, never resolve into Running without
// an operator fixing the underlying cause (bad image reference, missing
// registry credentials, a crashing entrypoint). waitForPodRunning fails fast
// on these instead of burning the full SandboxStartTimeout, so an operator
// sees "ImagePullBackOff: ..." instead of a bare "context deadline exceeded"
// two minutes later.
var k8sStuckWaitingReasons = map[string]bool{
	"ErrImagePull":               true,
	"ImagePullBackOff":           true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CrashLoopBackOff":           true,
}

// k8sPodWaitingReason reports the first container Waiting reason/message on
// pod, if any, and whether that reason is one waitForPodRunning treats as
// stuck (see k8sStuckWaitingReasons).
func k8sPodWaitingReason(pod *corev1.Pod) (reason, message string, stuck bool) {
	for _, status := range pod.Status.ContainerStatuses {
		if waiting := status.State.Waiting; waiting != nil && waiting.Reason != "" {
			return waiting.Reason, waiting.Message, k8sStuckWaitingReasons[waiting.Reason]
		}
	}
	return "", "", false
}

// k8sClientEntry is one lazily-built, cached client for a single kubeconfig
// context (see k8sRuntime.client).
type k8sClientEntry struct {
	clientset  kubernetes.Interface
	restConfig *rest.Config
}

type k8sRuntime struct {
	config           *appconfig.Config
	defaultNamespace string

	// clients caches one client per kubeconfig context, keyed by context
	// name ("" is the daemon-wide default: current-context / in-cluster
	// config). Agents can override which context they run in via
	// driver.k8s.context, so a single cached client is not enough once more
	// than one context is in play - each distinct context needs its own
	// client, built once and reused (see client below).
	clientsMu sync.Mutex
	clients   map[string]*k8sClientEntry

	// newExecutor builds the transport for one Kubernetes exec subresource
	// call. Defaults to remotecommand.NewSPDYExecutor; overridden in tests
	// to exercise execWithInput/execRaw without a real apiserver.
	newExecutor func(*rest.Config, string, *url.URL) (remotecommand.Executor, error)

	// proxyStateReader fetches a sandbox's persisted ProxyState (see the
	// ProxyStateReader doc comment for why createPod needs this instead of
	// trusting whatever proxyState an individual EnsureSandbox call
	// received). May be nil - callers fall back to the ad-hoc proxyState
	// parameter in that case (see resolveProxyState).
	proxyStateReader ProxyStateReader
}

// newK8sRuntime builds a Kubernetes Pod runtime driver. It does not touch the
// cluster or the filesystem at construction time: agent-compose always
// constructs every compiled driver at startup regardless of which one is
// selected, so building the kubeconfig-backed client eagerly here would break
// startup for anyone who compiled in k8s support but runs a different
// driver. Clients are built lazily on first use instead.
func newK8sRuntime(config *appconfig.Config, proxyStateReader ProxyStateReader) (SandboxRuntime, error) {
	// config.K8sNamespace is never empty here: pkg/config already defaults it
	// to "default" when K8S_NAMESPACE is unset.
	return &k8sRuntime{
		config:           config,
		defaultNamespace: strings.TrimSpace(config.K8sNamespace),
		clients:          make(map[string]*k8sClientEntry),
		newExecutor:      remotecommand.NewSPDYExecutor,
		proxyStateReader: proxyStateReader,
	}, nil
}

// resolveProxyState is the authoritative source for "should this sandbox's
// Pod run jupyter, and with what config" - see the ProxyStateReader doc
// comment. fallback is whatever proxyState the caller happened to receive;
// it's only used when there is no reader, or the reader errors (e.g. no
// ProxyState has been persisted for this sandbox yet).
func (r *k8sRuntime) resolveProxyState(sandboxID string, fallback ProxyState) ProxyState {
	if r.proxyStateReader == nil {
		return fallback
	}
	proxyState, err := r.proxyStateReader(sandboxID)
	if err != nil {
		return fallback
	}
	return proxyState
}

// namespaceFor resolves the namespace a sandbox's Pods live in: the
// sandbox's driver.k8s.namespace override if set, otherwise the daemon-wide
// K8S_NAMESPACE default.
func (r *k8sRuntime) namespaceFor(vmState VMState) string {
	return firstNonEmpty(strings.TrimSpace(vmState.K8sNamespace), r.defaultNamespace)
}

// client lazily builds and caches one Kubernetes client per kubeconfig
// context, so agents whose driver.k8s.context differs from each other (or
// from the daemon default) are each routed to the right cluster instead of
// silently reusing whichever client happened to be built first. Cluster auth
// for a given context follows kubectl's own convention: an explicit
// K8S_KUBECONFIG/KUBECONFIG path if set, otherwise the default
// ~/.kube/config loading rules, otherwise in-cluster config
// (clientcmd.DeferredLoadingClientConfig falls back to in-cluster on an
// empty merged config, same as clientcmd.BuildConfigFromFlags does).
func (r *k8sRuntime) client(contextName string) (kubernetes.Interface, *rest.Config, error) {
	contextName = strings.TrimSpace(contextName)

	r.clientsMu.Lock()
	defer r.clientsMu.Unlock()
	if entry, ok := r.clients[contextName]; ok {
		return entry.clientset, entry.restConfig, nil
	}

	// NewDefaultClientConfigLoadingRules, not a bare &ClientConfigLoadingRules{}:
	// only the constructor populates Precedence with the ~/.kube/config
	// fallback (and folds in KUBECONFIG) - a struct literal leaves Precedence
	// nil, so with no explicit path this would resolve to "no configuration
	// has been provided" instead of ever reaching the default file or the
	// in-cluster fallback below.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if explicitPath := strings.TrimSpace(r.config.K8sKubeconfigPath); explicitPath != "" {
		loadingRules.ExplicitPath = explicitPath
	}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	// A failed build is not cached: it's usually transient (kubeconfig
	// mounted but not yet written, a context temporarily unreachable), and
	// this map has no TTL/invalidation - caching the error would make a
	// one-time failure permanent for the rest of the daemon's process
	// lifetime, surviving even after whatever caused it is fixed.
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load kubernetes client config for context %q: verify K8S_KUBECONFIG/KUBECONFIG: %w", contextName, err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("build kubernetes client for context %q: %w", contextName, err)
	}
	entry := &k8sClientEntry{clientset: clientset, restConfig: restConfig}
	r.clients[contextName] = entry
	return entry.clientset, entry.restConfig, nil
}

func (r *k8sRuntime) EnsureSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState, proxyState ProxyState) (SandboxVMInfo, error) {
	if _, err := sandboxWorkspaceMount(sandbox, RuntimeDriverK8s); err != nil {
		return SandboxVMInfo{}, err
	}
	clientset, _, err := r.client(vmState.K8sContext)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	// Resolved once and reused for the create decision, the mismatch check
	// below, and the final result, rather than re-resolving in each of
	// createPod/ensureSandboxResult separately - see the ProxyStateReader
	// doc comment for why this must come from the store, not the proxyState
	// parameter this call happened to receive.
	effective := r.resolveProxyState(sandbox.Summary.ID, proxyState)
	pod, ok, err := r.findPod(ctx, clientset, sandbox, vmState)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	if ok {
		if reason, stale := k8sPodNeedsRecreate(pod, effective); stale {
			// Either a Failed/Succeeded Pod (terminal - the only way back to
			// Running is delete+recreate, Pods are immutable) or a Pod
			// whose baked-in Command doesn't match whether jupyter should
			// be running now (created before jupyter was enabled for this
			// sandbox - including every k8s Pod from before this field
			// existed - or before this sandbox's config turned it on).
			// Without this, a jupyter-mismatched Pod stays Running forever:
			// it never hits the terminal-Pod path, so nothing ever deletes
			// it, and every future EnsureSandbox call (including the ones
			// WriteGuestFile/WriteGuestDir trigger internally) would block
			// for the full jupyter-readiness timeout and fail, over and
			// over, with no way to self-heal.
			//
			// GracePeriodSeconds: new(int64) (i.e. 0) - unlike the terminal
			// case, a jupyter-mismatched Pod can still have a live
			// container (its whole point is being discarded regardless of
			// what's running inside it), and a default graceful delete
			// waits out terminationGracePeriodSeconds (30s unless set,
			// which this Pod spec doesn't) before the object actually
			// disappears. createPod below reuses this same sandbox's
			// deterministic Pod name, and Kubernetes rejects a Create while
			// the old object with that name still exists (Terminating) -
			// force-deleting shortens that window, but Delete returning
			// still doesn't guarantee the name is free yet, hence the
			// explicit wait below rather than proceeding straight to
			// createPod.
			if err := clientset.CoreV1().Pods(r.namespaceFor(vmState)).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: new(int64)}); err != nil && !apierrors.IsNotFound(err) {
				return SandboxVMInfo{}, fmt.Errorf("delete stale k8s pod %s (%s): %w", pod.Name, reason, err)
			}
			if err := r.waitForPodDeleted(ctx, clientset, r.namespaceFor(vmState), pod.Name, k8sPodDeleteTimeout); err != nil {
				return SandboxVMInfo{}, fmt.Errorf("wait for stale k8s pod %s (%s) deletion: %w", pod.Name, reason, err)
			}
			ok = false
		}
	}
	created := false
	if !ok {
		pod, err = r.createPod(ctx, clientset, sandbox, vmState, effective)
		if err != nil {
			return SandboxVMInfo{}, err
		}
		created = true
	}
	if pod.Status.Phase != corev1.PodRunning {
		attempt := k8sEnsureAttemptState{Namespace: r.namespaceFor(vmState), PodName: pod.Name, Created: created}
		pod, err = r.waitForPodRunning(ctx, clientset, attempt.Namespace, attempt.PodName)
		if err != nil {
			return SandboxVMInfo{}, r.cleanupK8sPodAfterEnsureFailure(ctx, clientset, attempt, err)
		}
	}
	return r.ensureSandboxResult(ctx, clientset, r.namespaceFor(vmState), pod, effective)
}

// k8sPodNeedsRecreate reports whether pod cannot serve as-is and must be
// deleted and recreated: it's in a terminal phase, or its baked-in Command
// no longer matches whether jupyter should be running (see the
// k8sSandboxLabelJupyter doc comment - a Pod's Command can't be changed in
// place). reason is a short human-readable cause for the delete-failure
// error message.
func k8sPodNeedsRecreate(pod *corev1.Pod, effective ProxyState) (reason string, stale bool) {
	if k8sPodIsTerminal(pod) {
		return "terminal phase", true
	}
	if k8sPodJupyterLabel(pod) != jupyterEnabled(effective) {
		return "jupyter config no longer matches the Pod's command", true
	}
	return "", false
}

// ensureSandboxResult builds EnsureSandbox's return value once pod is
// Running. When jupyter is enabled, this also waits for it to actually be
// answering before reporting success, mirroring the microsandbox driver.
//
// A Pod whose jupyter never comes up is force-deleted here rather than left
// in place: k8sPodNeedsRecreate only compares Phase and the jupyter label,
// both of which still read "fine" on a Pod stuck like this (Running,
// jupyter label matching effective) - so without this, every future
// EnsureSandbox call for this sandbox (including the ones
// WriteGuestFile/WriteGuestDir trigger internally) would burn a full
// JupyterReadyTimeout and fail again, forever, with no way to self-heal
// short of a manual kubectl delete. This applies regardless of whether this
// call created pod itself or found it already Running: a Pod that can't
// bring up jupyter can't serve this sandbox either way.
//
// Gated on ctx (the caller's own context) still being fine, not just
// readyCtx: readyCtx is derived from ctx, so if the caller cancelled or its
// own deadline passed, waitForJupyterProxy fails the exact same way as a
// genuine JupyterReadyTimeout expiry - but that failure says nothing about
// whether this Pod's jupyter would have come up given more time, so it
// must not be punished for a timeout that was never its own.
func (r *k8sRuntime) ensureSandboxResult(ctx context.Context, clientset kubernetes.Interface, namespace string, pod *corev1.Pod, effective ProxyState) (SandboxVMInfo, error) {
	if !jupyterEnabled(effective) {
		return SandboxVMInfo{BoxID: pod.Name}, nil
	}
	effective = k8sJupyterProxyStateForPod(effective, pod)
	readyCtx, cancel := context.WithTimeout(ctx, r.config.JupyterReadyTimeout)
	defer cancel()
	if err := waitForJupyterProxy(readyCtx, effective); err != nil {
		cause := fmt.Errorf("wait for jupyter on k8s pod %s: %w", pod.Name, err)
		if ctx.Err() != nil {
			return SandboxVMInfo{}, cause
		}
		// Force-delete and wait for the Pod to actually be gone (mirroring
		// the stale-recreate path above) rather than just requesting
		// deletion: a caller that retries immediately would otherwise find
		// this same stuck Pod again (still Terminating, still "fine" to
		// k8sPodNeedsRecreate) or have its createPod call hit AlreadyExists
		// on this Pod's deterministic name.
		//
		// Once ctx.Err() above has decided this is a genuine Pod-side
		// failure (not the caller giving up), the cleanup itself must not
		// still be at the mercy of ctx: waitForPodDeleted can poll for up
		// to k8sPodDeleteTimeout (30s), and ctx cancelling partway through
		// that window would abort the cleanup with the Pod still
		// Terminating - reintroducing the exact stuck state this whole
		// path exists to fix. cleanupCtx detaches from ctx the same way
		// k8sCleanupTimeout's callers already do.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), k8sPodDeleteTimeout)
		defer cancelCleanup()
		if delErr := clientset.CoreV1().Pods(namespace).Delete(cleanupCtx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: new(int64)}); delErr != nil && !apierrors.IsNotFound(delErr) {
			return SandboxVMInfo{}, fmt.Errorf("%w; cleanup jupyter-unready k8s pod %s: %w", cause, pod.Name, delErr)
		}
		if waitErr := r.waitForPodDeleted(cleanupCtx, clientset, namespace, pod.Name, k8sPodDeleteTimeout); waitErr != nil {
			return SandboxVMInfo{}, fmt.Errorf("%w; wait for jupyter-unready k8s pod %s deletion: %w", cause, pod.Name, waitErr)
		}
		return SandboxVMInfo{}, cause
	}
	return SandboxVMInfo{BoxID: pod.Name, ProxyState: &effective}, nil
}

// k8sJupyterProxyStateForPod finalizes proxyState's connect target once pod
// is Running: GuestHost is the Pod's own address on the pod network - the
// daemon (itself in-cluster, per this driver's Helm chart) reaches it
// directly, with no port-publish/port-forward step needed. Split out from
// ensureSandboxResult so this part is testable without a live network probe.
func k8sJupyterProxyStateForPod(proxyState ProxyState, pod *corev1.Pod) ProxyState {
	proxyState.Enabled = true
	proxyState.GuestHost = pod.Status.PodIP
	return proxyState
}

// k8sEnsureAttemptState describes how far a failed EnsureSandbox attempt
// progressed, for cleanupK8sPodAfterEnsureFailure.
type k8sEnsureAttemptState struct {
	Namespace string
	PodName   string
	Created   bool
}

// cleanupK8sPodAfterEnsureFailure force-deletes the Pod this EnsureSandbox
// call itself just created when it fails to reach Running, mirroring the
// docker driver's cleanupDockerContainerAfterEnsureFailure. A Pod that
// EnsureSandbox merely found (Created == false) is left alone, since some
// other actor owns its lifecycle. Without this, a sandbox that repeatedly
// fails to start (bad image, insufficient cluster capacity) would leave an
// ever-growing trail of Pending/Failed Pods behind in the cluster.
func (r *k8sRuntime) cleanupK8sPodAfterEnsureFailure(ctx context.Context, clientset kubernetes.Interface, attempt k8sEnsureAttemptState, cause error) error {
	if !attempt.Created || cause == nil {
		return cause
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), k8sCleanupTimeout)
	defer cancel()
	// GracePeriodSeconds: new(int64) (i.e. 0) - a plain Delete only requests
	// graceful termination and waits out terminationGracePeriodSeconds (30s
	// unless set, which this Pod spec doesn't) before the object actually
	// disappears. Left at the default, a Pod that failed to reach Running
	// stays visible as Terminating for that whole window: findPod would
	// still report it ok on a call that lands inside it, and since
	// k8sPodNeedsRecreate has no notion of "already being deleted", that
	// call would wait out this same waitForPodRunning failure again instead
	// of proceeding straight to createPod.
	if err := clientset.CoreV1().Pods(attempt.Namespace).Delete(cleanupCtx, attempt.PodName, metav1.DeleteOptions{GracePeriodSeconds: new(int64)}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("%w; cleanup newly created k8s pod %s: %w", cause, attempt.PodName, err)
	}
	return cause
}

func (r *k8sRuntime) createPod(ctx context.Context, clientset kubernetes.Interface, sandbox *Sandbox, vmState VMState, effective ProxyState) (*corev1.Pod, error) {
	name := r.podName(sandbox, vmState)
	image := resolveSandboxGuestImage(vmState.Image, sandbox.Summary.GuestImage, defaultGuestImageForDriver(r.config, RuntimeDriverK8s))
	if image == "" {
		return nil, fmt.Errorf("k8s sandbox %s has no guest image configured", sandbox.Summary.ID)
	}
	volumes, volumeMounts, err := r.podVolumeSpecs(sandbox, vmState)
	if err != nil {
		return nil, err
	}
	// A Pod's Command is fixed for its whole life, so the jupyter decision
	// has to be made once, correctly, right here (effective is already
	// resolved via resolveProxyState by the caller - see the
	// ProxyStateReader doc comment). k8sSandboxLabelJupyter records that
	// decision on the Pod itself so a later EnsureSandbox call can detect
	// whether it's stale (see k8sPodNeedsRecreate) without having to infer
	// it from the live Command string.
	cmdText := "tail -f /dev/null"
	var containerPorts []corev1.ContainerPort
	if jupyterEnabled(effective) {
		cmdText = jupyterLaunchCommand(r.config, effective, false)
		containerPorts = []corev1.ContainerPort{{ContainerPort: int32(effective.GuestPort)}}
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.namespaceFor(vmState),
			Annotations: map[string]string{
				k8sSandboxLabelID: sandbox.Summary.ID,
			},
			Labels: map[string]string{
				k8sSandboxLabelID:      k8sSandboxLabelValue(sandbox.Summary.ID),
				k8sSandboxLabelDriver:  RuntimeDriverK8s,
				k8sSandboxLabelJupyter: strconv.FormatBool(jupyterEnabled(effective)),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:            k8sContainerName,
					Image:           image,
					ImagePullPolicy: k8sImagePullPolicy(sandbox.Summary.PullPolicy),
					Command:         []string{"sh", "-lc", cmdText},
					Ports:           containerPorts,
					WorkingDir:      r.config.GuestWorkspacePath,
					Env:             k8sEnvVars(r.containerEnv(sandbox)),
					VolumeMounts:    volumeMounts,
				},
			},
			Volumes: volumes,
		},
	}
	created, err := clientset.CoreV1().Pods(r.namespaceFor(vmState)).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create k8s pod for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	return created, nil
}

func (r *k8sRuntime) podVolumeSpecs(sandbox *Sandbox, vmState VMState) ([]corev1.Volume, []corev1.VolumeMount, error) {
	if sandbox == nil || len(sandbox.VolumeMounts) == 0 {
		return nil, nil, nil
	}
	appconfig.ApplyDefaultGuestPaths(r.config)
	namespace := r.namespaceFor(vmState)
	volumes := make([]corev1.Volume, 0, len(sandbox.VolumeMounts))
	mounts := make([]corev1.VolumeMount, 0, len(sandbox.VolumeMounts))
	volumeNames := make(map[string]string, len(sandbox.VolumeMounts))
	for _, mount := range sandbox.VolumeMounts {
		mountType := strings.ToLower(strings.TrimSpace(mount.Type))
		if mountType == "bind" {
			return nil, nil, fmt.Errorf("k8s sandbox %s does not support bind mounts", sandbox.Summary.ID)
		}
		if mountType != "volume" {
			return nil, nil, fmt.Errorf("k8s sandbox %s has unsupported mount type %q", sandbox.Summary.ID, mount.Type)
		}
		if strings.TrimSpace(mount.Driver) != RuntimeDriverK8s {
			return nil, nil, fmt.Errorf("k8s sandbox %s volume %q uses unsupported driver %q", sandbox.Summary.ID, mount.Source, mount.Driver)
		}
		if err := k8sValidateVolumeMountTarget(mount.Target, r.config.GuestWorkspacePath, r.config.GuestHomePath); err != nil {
			return nil, nil, fmt.Errorf("k8s sandbox %s volume %q: %w", sandbox.Summary.ID, mount.Source, err)
		}
		claimNamespace, claimName, err := parseK8sPVCRef(mount.HostPath)
		if err != nil {
			return nil, nil, fmt.Errorf("k8s sandbox %s volume %q: %w", sandbox.Summary.ID, mount.Source, err)
		}
		if claimNamespace != namespace {
			return nil, nil, fmt.Errorf("k8s sandbox %s volume %q is in namespace %q, Pod is in namespace %q", sandbox.Summary.ID, mount.Source, claimNamespace, namespace)
		}
		ref := claimNamespace + "/" + claimName
		volumeName := volumeNames[ref]
		if volumeName == "" {
			volumeName = k8sPodVolumeName(claimNamespace, claimName)
			volumeNames[ref] = volumeName
			volumes = append(volumes, corev1.Volume{Name: volumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName}}})
		}
		mounts = append(mounts, corev1.VolumeMount{Name: volumeName, MountPath: mount.Target, ReadOnly: mount.ReadOnly})
	}
	return volumes, mounts, nil
}

// k8sValidateVolumeMountTarget rejects a volume mount target that partially
// overlaps GuestWorkspacePath or GuestHomePath - a strict ancestor or
// descendant of either, short of an exact match. WriteGuestDir syncs those
// two paths wholesale (rm -rf the guest side, then restore from the
// daemon's host-side snapshot); a mount landing anywhere inside one of them
// other than exactly at its root would force a choice between destroying
// the mounted volume's content (deleting under it) or silently skipping the
// sync of everything else in that directory (leaving it stale) - neither
// acceptable, so this configuration is rejected up front instead. Mounting
// exactly at the workspace or home root is fine: WriteGuestDir already
// treats that whole push as a no-op in favor of the volume's own content
// (see k8sGuestDirVolumeMountOverlapKind). A target unrelated to either path is
// always fine too.
func k8sValidateVolumeMountTarget(target string, guestPaths ...string) error {
	target = filepath.Clean(strings.TrimSpace(target))
	for _, guestPath := range guestPaths {
		guestPath = strings.TrimSpace(guestPath)
		if guestPath == "" {
			continue
		}
		guestPath = filepath.Clean(guestPath)
		if target == guestPath {
			continue
		}
		if k8sPathIsWithin(target, guestPath) || k8sPathIsWithin(guestPath, target) {
			return fmt.Errorf("mount target %q partially overlaps %q; mount the whole directory instead of a sub-path, or choose a target outside it", target, guestPath)
		}
	}
	return nil
}

func parseK8sPVCRef(value string) (string, string, error) {
	namespace, claimName, ok := strings.Cut(strings.TrimSpace(value), "/")
	if !ok || namespace == "" || claimName == "" || strings.Contains(claimName, "/") {
		return "", "", fmt.Errorf("PVC source %q must be namespace/claim", value)
	}
	return namespace, claimName, nil
}

func k8sPodVolumeName(namespace, claimName string) string {
	digest := sha256.Sum256([]byte(namespace + "/" + claimName))
	return "volume-" + hex.EncodeToString(digest[:])[:16]
}

func (r *k8sRuntime) containerEnv(sandbox *Sandbox) map[string]string {
	appconfig.ApplyDefaultGuestPaths(r.config)
	env := sandboxEnvMap(sandbox.EnvItems, sandbox.RuntimeEnvItems)
	if env == nil {
		env = map[string]string{}
	}
	env["SANDBOX_ID"] = sandbox.Summary.ID
	env["WORKSPACE"] = r.config.GuestWorkspacePath
	env["STATE_ROOT"] = r.config.GuestStateRoot
	env["RUNTIME_ROOT"] = r.config.GuestRuntimeRoot
	// K8S_RUNTIME_BASE_URL overrides the daemon-wide AGENT_COMPOSE_RUNTIME_BASE_URL
	// for k8s sandboxes only - see docs/design/k8s_pod_runtime_driver_design.md §2.2.
	if base := strings.TrimRight(strings.TrimSpace(r.config.K8sRuntimeBaseURL), "/"); base != "" {
		env["AGENT_COMPOSE_RUNTIME_BASE_URL"] = base
	}
	return env
}

func k8sEnvVars(env map[string]string) []corev1.EnvVar {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	vars := make([]corev1.EnvVar, 0, len(keys))
	for _, key := range keys {
		vars = append(vars, corev1.EnvVar{Name: key, Value: env[key]})
	}
	return vars
}

func k8sImagePullPolicy(pullPolicy string) corev1.PullPolicy {
	switch strings.ToLower(strings.TrimSpace(pullPolicy)) {
	case "always":
		return corev1.PullAlways
	case "never":
		return corev1.PullNever
	default:
		return corev1.PullIfNotPresent
	}
}

// k8sPodIsTerminal reports whether a Pod has reached a terminal phase
// (Failed or Succeeded) and therefore can never transition to Running again
// - Pods, unlike containers, have no restart-in-place primitive once
// terminal.
func k8sPodIsTerminal(pod *corev1.Pod) bool {
	switch pod.Status.Phase {
	case corev1.PodFailed, corev1.PodSucceeded:
		return true
	default:
		return false
	}
}

// k8sPodJupyterLabel reads back what createPod recorded via
// k8sSandboxLabelJupyter. A missing or unparseable label reads as false,
// which is correct for any Pod created before this label existed.
func k8sPodJupyterLabel(pod *corev1.Pod) bool {
	enabled, _ := strconv.ParseBool(pod.Labels[k8sSandboxLabelJupyter])
	return enabled
}

func (r *k8sRuntime) waitForPodRunning(ctx context.Context, clientset kubernetes.Interface, namespace, name string) (*corev1.Pod, error) {
	timeout := r.config.SandboxStartTimeout
	if timeout <= 0 {
		timeout = k8sDefaultStartTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(k8sPodPollInterval)
	defer ticker.Stop()
	for {
		pod, err := clientset.CoreV1().Pods(namespace).Get(waitCtx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("inspect k8s pod %s: %w", name, err)
		}
		if pod.Status.Phase == corev1.PodRunning {
			return pod, nil
		}
		if k8sPodIsTerminal(pod) {
			return nil, fmt.Errorf("k8s pod %s exited before becoming ready: phase=%s", name, pod.Status.Phase)
		}
		if reason, message, stuck := k8sPodWaitingReason(pod); stuck {
			return nil, fmt.Errorf("k8s pod %s container %s will not start: %s: %s", name, k8sContainerName, reason, strings.TrimSpace(message))
		}
		select {
		case <-waitCtx.Done():
			if reason, message, _ := k8sPodWaitingReason(pod); reason != "" {
				return nil, fmt.Errorf("wait for k8s pod %s to be running: %w: container %s is %s: %s", name, waitCtx.Err(), k8sContainerName, reason, strings.TrimSpace(message))
			}
			return nil, fmt.Errorf("wait for k8s pod %s to be running: %w", name, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// waitForPodDeleted polls until name no longer exists in namespace, or
// timeout elapses. A Kubernetes Pod object is not removed the instant
// Delete returns - even force-deleted (GracePeriodSeconds=0) - so a caller
// about to Create a new Pod under the same deterministic name must wait for
// this first, or risk a 409 AlreadyExists against the still-Terminating
// old object.
func (r *k8sRuntime) waitForPodDeleted(ctx context.Context, clientset kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(k8sPodPollInterval)
	defer ticker.Stop()
	for {
		_, err := clientset.CoreV1().Pods(namespace).Get(waitCtx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect k8s pod %s: %w", name, err)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for k8s pod %s to be deleted: %w", name, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// podName picks the sandbox Pod's name. The generated fallback is prefixed
// "agent-compose-sandbox-" rather than bare "agent-compose-" so it reads
// distinctly from the daemon's own Deployment-managed Pod name (which
// Kubernetes derives from the Helm release name, typically also
// "agent-compose-<hash>-<random>") in a plain `kubectl get pods`.
func (r *k8sRuntime) podName(sandbox *Sandbox, vmState VMState) string {
	return firstNonEmpty(strings.TrimSpace(vmState.BoxName), strings.TrimSpace(sandbox.Summary.RuntimeRef), "agent-compose-sandbox-"+sanitizeDockerContainerName(sandbox.Summary.ID))
}

// findPod resolves a sandbox's Pod by previously known name, then by the
// name agent-compose would create, then by label selector — mirroring the
// docker driver's ID -> name -> label lookup order (see findContainer).
func (r *k8sRuntime) findPod(ctx context.Context, clientset kubernetes.Interface, sandbox *Sandbox, vmState VMState) (*corev1.Pod, bool, error) {
	namespace := r.namespaceFor(vmState)
	for _, lookup := range []string{strings.TrimSpace(vmState.BoxID), r.podName(sandbox, vmState)} {
		if lookup == "" {
			continue
		}
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, lookup, metav1.GetOptions{})
		if err == nil {
			return pod, true, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, false, fmt.Errorf("inspect k8s pod %s: %w", lookup, err)
		}
	}

	selector := labels.Set{
		k8sSandboxLabelID:     k8sSandboxLabelValue(sandbox.Summary.ID),
		k8sSandboxLabelDriver: RuntimeDriverK8s,
	}.AsSelector().String()
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, false, fmt.Errorf("list k8s pods for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	if len(pods.Items) == 0 {
		return nil, false, nil
	}
	return &pods.Items[0], true, nil
}

// k8sSandboxLabelValue returns a stable Kubernetes label value for a sandbox
// ID. Native sandbox IDs are 64-character SHA-256 hex strings, one byte longer
// than Kubernetes permits in a label value. Preserve shorter legacy IDs when
// they are already valid and hash every other representation into a compact,
// collision-resistant value. The full ID remains available in the Pod
// annotation with the same key.
func k8sSandboxLabelValue(sandboxID string) string {
	sandboxID = strings.TrimSpace(sandboxID)
	if validationErrors := validation.IsLabelValue(sandboxID); len(validationErrors) == 0 {
		return sandboxID
	}
	digest := sha256.Sum256([]byte(sandboxID))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])
	return "sha256-" + strings.ToLower(encoded)
}

// IsSandboxAlive reports whether the sandbox's Pod still exists and is
// Running, letting Lifecycle.ReconcileRuntimeState self-heal the same way it
// already does for the docker/boxlite/microsandbox drivers (see
// pkg/sandboxes/lifecycle.go). Without this, a Pod that disappears out from
// under the daemon - node eviction, an OOM kill, an operator running
// `kubectl delete pod` - leaves VMStatusRunning stuck stale in daemon state
// until some unrelated call happens to invoke findPod, which silently
// recreates an empty Pod under the same name.
func (r *k8sRuntime) IsSandboxAlive(ctx context.Context, sandbox *Sandbox, vmState VMState) (bool, error) {
	clientset, _, err := r.client(vmState.K8sContext)
	if err != nil {
		return false, err
	}
	pod, ok, err := r.findPod(ctx, clientset, sandbox, vmState)
	if err != nil || !ok {
		return false, err
	}
	return pod.Status.Phase == corev1.PodRunning, nil
}

func (r *k8sRuntime) StopSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState) (bool, error) {
	return r.deletePod(ctx, sandbox, vmState)
}

func (r *k8sRuntime) RemoveSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState) error {
	_, err := r.deletePod(ctx, sandbox, vmState)
	return err
}

// deletePod deletes the sandbox's Pod, reporting true when it was already
// gone. Pods have no stop/restart primitive distinct from deletion, so
// StopSandbox and RemoveSandbox resolve to the same operation for this
// driver in v1.
func (r *k8sRuntime) deletePod(ctx context.Context, sandbox *Sandbox, vmState VMState) (bool, error) {
	clientset, _, err := r.client(vmState.K8sContext)
	if err != nil {
		return false, err
	}
	pod, ok, err := r.findPod(ctx, clientset, sandbox, vmState)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if err := clientset.CoreV1().Pods(r.namespaceFor(vmState)).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("delete k8s pod %s: %w", pod.Name, err)
	}
	return false, nil
}

// k8sExecRequest bundles the target and command for one k8s exec/execWithStream call.
type k8sExecRequest struct {
	Sandbox *Sandbox
	VMState VMState
	Spec    ExecSpec
}

func (r *k8sRuntime) Exec(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec) (ExecResult, error) {
	return r.execWithInput(ctx, k8sExecRequest{Sandbox: sandbox, VMState: vmState, Spec: spec}, nil, nil)
}

func (r *k8sRuntime) ExecStream(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	return r.execWithInput(ctx, k8sExecRequest{Sandbox: sandbox, VMState: vmState, Spec: spec}, nil, stream)
}

// execWithInput is the k8s driver's internal data-transfer boundary. Public
// Exec calls leave stdin nil; guest file pushes stream bytes through the
// Kubernetes exec subresource without putting payloads in argv.
func (r *k8sRuntime) execWithInput(ctx context.Context, request k8sExecRequest, stdin io.Reader, stream ExecStreamWriter) (ExecResult, error) {
	executor, err := r.newExecutorFor(ctx, request, stdin != nil)
	if err != nil {
		return ExecResult{}, err
	}

	collector := &dockerExecCollector{stream: stream, filter: newExecOutputFilter()}
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: &dockerExecWriter{collector: collector, stream: StdioStdout},
		Stderr: &dockerExecWriter{collector: collector, stream: StdioStderr},
	})
	collector.finish()

	if ctx.Err() != nil {
		return ExecResult{}, ctx.Err()
	}

	exitCode, err := k8sExitCode(streamErr, request.Sandbox.Summary.ID)
	if err != nil {
		return ExecResult{}, err
	}

	result := ExecResult{
		ExitCode: exitCode,
		Stdout:   collector.stdout.String(),
		Stderr:   collector.stderr.String(),
		Output:   collector.output.String(),
	}
	result.Success = result.ExitCode == 0
	return result, nil
}

// execRaw runs a k8s exec and returns raw, undecoded stdout/stderr bytes and
// the exit code, bypassing the UTF-8 streaming decoder that
// execWithInput/dockerExecCollector applies. That decoder replaces invalid
// UTF-8 byte sequences with U+FFFD, which is correct for human-readable
// stdout/stderr display but silently corrupts arbitrary binary content (a
// PNG, a zip, any non-text artifact). ReadGuestFile and ReadGuestDir pull
// exactly that kind of arbitrary guest-written content back through this
// same exec subresource - see docs/design/k8s_pod_runtime_driver_design.md
// §2.1, the k8s driver has no shared filesystem to read files from - so they
// use this path instead of execWithInput.
func (r *k8sRuntime) execRaw(ctx context.Context, request k8sExecRequest, stdin io.Reader) (stdout, stderr []byte, exitCode int, err error) {
	executor, err := r.newExecutorFor(ctx, request, stdin != nil)
	if err != nil {
		return nil, nil, 0, err
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: &stdoutBuf,
		Stderr: &stderrBuf,
	})

	if ctx.Err() != nil {
		return nil, nil, 0, ctx.Err()
	}

	exitCode, err = k8sExitCode(streamErr, request.Sandbox.Summary.ID)
	if err != nil {
		return nil, nil, 0, err
	}
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), exitCode, nil
}

// newExecutorFor resolves the sandbox's Pod and builds the exec transport
// for one k8s exec subresource call, shared by execWithInput and execRaw.
func (r *k8sRuntime) newExecutorFor(ctx context.Context, request k8sExecRequest, withStdin bool) (remotecommand.Executor, error) {
	sandbox, vmState, spec := request.Sandbox, request.VMState, request.Spec
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return nil, fmt.Errorf("k8s exec command is required")
	}

	clientset, restConfig, err := r.client(vmState.K8sContext)
	if err != nil {
		return nil, err
	}
	pod, ok, err := r.findPod(ctx, clientset, sandbox, vmState)
	if err != nil {
		return nil, err
	}
	if !ok || pod.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("k8s pod for sandbox %s is not running", sandbox.Summary.ID)
	}

	// PodExecOptions has no cwd/env fields (unlike Docker's exec API), so both
	// are folded into the shell script the container runs.
	//
	// The exec subresource request is built from a dedicated RESTClient
	// (k8sExecRESTClient) rather than clientset.CoreV1().RESTClient(): the
	// latter is what a real cluster client returns, but k8s.io/client-go's
	// fake.Clientset (used throughout this package's tests) always returns a
	// nil RESTClient there, which would panic on the very first .Post() call.
	// Building it directly from restConfig keeps behavior against a real
	// cluster identical while making this path exercisable in tests.
	restClient, err := k8sExecRESTClient(restConfig)
	if err != nil {
		return nil, fmt.Errorf("build k8s exec client for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	script := k8sExecScript(spec, r.config.GuestWorkspacePath)
	req := restClient.Post().
		Resource("pods").
		Namespace(r.namespaceFor(vmState)).
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(k8sPodExecOptions(script, withStdin), scheme.ParameterCodec)

	executor, err := r.newExecutor(restConfig, http.MethodPost, req.URL())
	if err != nil {
		return nil, fmt.Errorf("create k8s exec for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	return executor, nil
}

// k8sExecRESTClient builds a RESTClient scoped to the core/v1 API group,
// replicating the same defaults corev1client.NewForConfig applies
// internally (see k8s.io/client-go/kubernetes/typed/core/v1.setConfigDefaults),
// so its behavior against a real cluster matches clientset.CoreV1().RESTClient().
func k8sExecRESTClient(restConfig *rest.Config) (rest.Interface, error) {
	config := *restConfig
	gv := corev1.SchemeGroupVersion
	config.GroupVersion = &gv
	config.APIPath = "/api"
	config.NegotiatedSerializer = rest.CodecFactoryForGeneratedClient(scheme.Scheme, scheme.Codecs).WithoutConversion()
	if config.UserAgent == "" {
		config.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	return rest.RESTClientFor(&config)
}

// k8sExitCode turns the error StreamWithContext returns into an exit code:
// nil means success (0), a k8sexec.CodeExitError carries the guest process's
// real exit code, anything else is a transport/protocol failure.
func k8sExitCode(streamErr error, sandboxID string) (int, error) {
	if streamErr == nil {
		return 0, nil
	}
	var codeErr k8sexec.CodeExitError
	if !errors.As(streamErr, &codeErr) {
		return 0, fmt.Errorf("k8s exec for sandbox %s: %w", sandboxID, streamErr)
	}
	return codeErr.Code, nil
}

func k8sPodExecOptions(script string, withStdin bool) *corev1.PodExecOptions {
	return &corev1.PodExecOptions{
		Container: k8sContainerName,
		Command:   []string{"sh", "-lc", script},
		Stdin:     withStdin,
		Stdout:    true,
		Stderr:    true,
	}
}

// k8sExecScript folds ExecSpec's working directory and environment into a
// shell script, since the Kubernetes exec subresource (unlike Docker's exec
// API) has no dedicated cwd/env fields.
func k8sExecScript(spec ExecSpec, defaultCwd string) string {
	var b strings.Builder
	// set -e, and cd as its own ";"-terminated statement rather than
	// "cd X && ...": bash's -e specifically exempts a non-final command in
	// an && / || list from triggering exit-on-error, so "cd X && export
	// Y" would still let a failed cd fall through to export/exec via the
	// later ";" separators - set -e only catches a failing cd if it's a
	// bare statement, not the left side of &&.
	b.WriteString("set -e; ")
	if cwd := firstNonEmpty(spec.Cwd, defaultCwd); cwd != "" {
		b.WriteString("cd " + shellQuote(cwd) + "; ")
	}
	keys := make([]string, 0, len(spec.Env))
	for key := range spec.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString("export " + key + "=" + shellQuote(spec.Env[key]) + "; ")
	}
	b.WriteString("exec " + shellQuote(spec.Command))
	for _, arg := range spec.Args {
		b.WriteString(" " + shellQuote(arg))
	}
	return b.String()
}
