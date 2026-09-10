//go:build k8scompose

package driver

import (
	"bytes"
	"context"
	"fmt"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	validation "k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// testK8sPodIP is the fake PodIP the create reactor in newTestK8sRuntime
// stamps onto every Pod it creates, standing in for a real kubelet
// assigning one - tests that resolve jupyter's GuestHost assert against
// this constant.
const testK8sPodIP = "10.42.0.7"

// newTestK8sRuntime builds a k8sRuntime backed by a fake clientset, bypassing
// the lazy clientcmd-based client() construction entirely. The fake clientset
// does not simulate a kubelet transitioning a Pod to Running, so a reactor
// marks every created Pod Running (with a fake PodIP) immediately.
func newTestK8sRuntime(objects ...runtime.Object) (*k8sRuntime, *fake.Clientset) {
	clientset := fake.NewClientset(objects...)
	clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		pod, ok := createAction.GetObject().(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		pod.Status.Phase = corev1.PodRunning
		pod.Status.PodIP = testK8sPodIP
		return false, nil, nil
	})
	r := &k8sRuntime{
		config: &appconfig.Config{
			GuestWorkspacePath:  "/workspace",
			GuestHomePath:       "/root",
			GuestStateRoot:      "/data/state",
			GuestRuntimeRoot:    "/data/runtime",
			GuestLogRoot:        "/data/logs",
			SandboxStartTimeout: 5 * time.Second,
			JupyterReadyTimeout: 5 * time.Second,
		},
		defaultNamespace: "default",
		clients: map[string]*k8sClientEntry{
			"": {clientset: clientset, restConfig: &rest.Config{Host: "https://k8s-runtime-test.invalid"}},
		},
	}
	return r, clientset
}

// newTestK8sRuntimeNoAutoRunning is newTestK8sRuntime without the reactor
// that fakes a kubelet transitioning a created Pod to Running, so tests can
// exercise waitForPodRunning's own polling/timeout/diagnostic behavior
// against a Pod that never becomes ready.
func newTestK8sRuntimeNoAutoRunning(objects ...runtime.Object) (*k8sRuntime, *fake.Clientset) {
	clientset := fake.NewClientset(objects...)
	r := &k8sRuntime{
		config: &appconfig.Config{
			GuestWorkspacePath:  "/workspace",
			GuestHomePath:       "/root",
			GuestStateRoot:      "/data/state",
			GuestRuntimeRoot:    "/data/runtime",
			GuestLogRoot:        "/data/logs",
			SandboxStartTimeout: 500 * time.Millisecond,
		},
		defaultNamespace: "default",
		clients: map[string]*k8sClientEntry{
			"": {clientset: clientset, restConfig: &rest.Config{Host: "https://k8s-runtime-test.invalid"}},
		},
	}
	return r, clientset
}

// fakeK8sExecutor stands in for remotecommand.NewSPDYExecutor's real SPDY
// transport in tests, letting k8sRuntime.newExecutor be swapped out to
// exercise execWithInput/execRaw without a live apiserver.
type fakeK8sExecutor struct {
	stdout []byte
	stderr []byte
	err    error
}

func (f fakeK8sExecutor) Stream(options remotecommand.StreamOptions) error {
	return f.StreamWithContext(context.Background(), options)
}

func (f fakeK8sExecutor) StreamWithContext(_ context.Context, options remotecommand.StreamOptions) error {
	if options.Stdout != nil && len(f.stdout) > 0 {
		if _, err := options.Stdout.Write(f.stdout); err != nil {
			return err
		}
	}
	if options.Stderr != nil && len(f.stderr) > 0 {
		if _, err := options.Stderr.Write(f.stderr); err != nil {
			return err
		}
	}
	return f.err
}

func testSandbox(t *testing.T, id string) *Sandbox {
	t.Helper()
	return &Sandbox{Summary: SandboxSummary{
		ID:            id,
		GuestImage:    "example.test/image:latest",
		WorkspacePath: filepath.Join(t.TempDir(), "workspace"),
	}}
}

func TestK8sEnsureSandboxCreatesPodWhenMissing(t *testing.T) {
	r, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-1")

	info, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
	if err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	if info.BoxID == "" {
		t.Fatal("EnsureSandbox() returned empty BoxID")
	}
	// "agent-compose-sandbox-", not bare "agent-compose-": the daemon's own
	// Deployment-managed Pod is typically also named "agent-compose-<hash>-
	// <random>", so a distinct prefix keeps the two apart in `kubectl get
	// pods` without requiring a label selector.
	if !strings.HasPrefix(info.BoxID, "agent-compose-sandbox-") {
		t.Fatalf("EnsureSandbox() BoxID = %q, want \"agent-compose-sandbox-\" prefix", info.BoxID)
	}

	pod, err := clientset.CoreV1().Pods("default").Get(context.Background(), info.BoxID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected created pod to exist: %v", err)
	}
	if pod.Labels[k8sSandboxLabelID] != sandbox.Summary.ID {
		t.Fatalf("pod label %s = %q, want %q", k8sSandboxLabelID, pod.Labels[k8sSandboxLabelID], sandbox.Summary.ID)
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != sandbox.Summary.GuestImage {
		t.Fatalf("pod container image = %+v, want %q", pod.Spec.Containers, sandbox.Summary.GuestImage)
	}
	// The k8s driver has no shared filesystem with the daemon (see
	// docs/design/k8s_pod_runtime_driver_design.md §2.1): sandbox data
	// flows over Exec, not a mount, so the Pod declares none.
	if len(pod.Spec.Volumes) != 0 {
		t.Fatalf("pod declares volumes %+v, want none", pod.Spec.Volumes)
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 0 {
		t.Fatalf("pod container declares volume mounts %+v, want none", pod.Spec.Containers[0].VolumeMounts)
	}
}

func TestK8sCreatePodLaunchesJupyterWhenEnabled(t *testing.T) {
	r, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-jupyter")

	// createPod trusts its effective parameter directly - resolving it from
	// the durable store (rather than an ad-hoc caller-supplied proxyState)
	// is EnsureSandbox's job, exercised by
	// TestK8sEnsureSandboxLaunchesJupyterEvenWhenPodFirstCreatedWithZeroProxyState
	// below.
	effective := ProxyState{Enabled: true, GuestPort: 8888, Token: "tok"}
	pod, err := r.createPod(context.Background(), clientset, sandbox, VMState{}, effective)
	if err != nil {
		t.Fatalf("createPod() error = %v", err)
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("pod containers = %+v, want exactly 1", pod.Spec.Containers)
	}
	container := pod.Spec.Containers[0]
	if len(container.Command) != 3 || !strings.Contains(container.Command[2], "jupyterlab") {
		t.Fatalf("pod command = %+v, want a jupyterlab launch command", container.Command)
	}
	if len(container.Ports) != 1 || container.Ports[0].ContainerPort != 8888 {
		t.Fatalf("pod container ports = %+v, want [{ContainerPort: 8888}]", container.Ports)
	}
	if pod.Labels[k8sSandboxLabelJupyter] != "true" {
		t.Fatalf("pod label %s = %q, want \"true\"", k8sSandboxLabelJupyter, pod.Labels[k8sSandboxLabelJupyter])
	}
}

// TestK8sEnsureSandboxRecreatesPodWhenJupyterConfigChanges is the
// regression test for the "jupyter enabled but Pod never got the jupyter
// command" failure mode: a Pod's Command is fixed at creation, so if the
// sandbox's persisted jupyter config changes afterward (or the Pod predates
// the k8sSandboxLabelJupyter label entirely), EnsureSandbox must detect the
// mismatch and recreate the Pod instead of endlessly waiting on a Pod that
// can never satisfy waitForJupyterProxy.
func TestK8sEnsureSandboxRecreatesPodWhenJupyterConfigChanges(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-jupyter-config-change")
	name := (&k8sRuntime{}).podName(sandbox, VMState{})

	t.Run("pod predates the jupyter label", func(t *testing.T) {
		existing := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels: map[string]string{
					k8sSandboxLabelID:     sandbox.Summary.ID,
					k8sSandboxLabelDriver: RuntimeDriverK8s,
					// No k8sSandboxLabelJupyter - as if this Pod predates
					// jupyter support entirely.
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		r, clientset := newTestK8sRuntime(existing)
		r.proxyStateReader = func(string) (ProxyState, error) {
			return ProxyState{Enabled: true, GuestPort: 8888, Token: "tok"}, nil
		}
		r.config.JupyterReadyTimeout = 50 * time.Millisecond
		// The fake cluster never answers waitForJupyterProxy, so this
		// EnsureSandbox call fails and the newly (re)created Pod is cleaned
		// up as part of that failure (see ensureSandboxResult) - capture the
		// recreated Pod's Command as it's created, before that cleanup runs,
		// rather than Get-ing it back afterward.
		var createdCommand []string
		clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
			createAction, ok := action.(k8stesting.CreateAction)
			if !ok {
				return false, nil, nil
			}
			pod, ok := createAction.GetObject().(*corev1.Pod)
			if !ok {
				return false, nil, nil
			}
			createdCommand = append([]string(nil), pod.Spec.Containers[0].Command...)
			return false, nil, nil
		})

		if _, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{}); err == nil {
			t.Fatal("EnsureSandbox() error = nil, want a jupyter-readiness failure (nothing is listening in this fake cluster)")
		}

		if len(createdCommand) != 3 || !strings.Contains(createdCommand[2], "jupyterlab") {
			t.Fatalf("recreated pod command = %+v, want a jupyterlab launch command (the mismatched Pod should have been recreated)", createdCommand)
		}
		if _, getErr := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(getErr) {
			t.Fatalf("get pod after jupyter-readiness failure: err = %v, want NotFound (a Pod that can't bring up jupyter must not be left behind)", getErr)
		}
	})

	t.Run("jupyter turned back off leaves a plain pod alone on retry, not stuck waiting", func(t *testing.T) {
		jupyterPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels: map[string]string{
					k8sSandboxLabelID:      sandbox.Summary.ID,
					k8sSandboxLabelDriver:  RuntimeDriverK8s,
					k8sSandboxLabelJupyter: "true",
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		r, clientset := newTestK8sRuntime(jupyterPod)
		r.proxyStateReader = func(string) (ProxyState, error) {
			return ProxyState{Enabled: false}, nil
		}

		info, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
		if err != nil {
			t.Fatalf("EnsureSandbox() error = %v, want the stale jupyter Pod replaced with a plain one", err)
		}
		if info.BoxID != name {
			t.Fatalf("EnsureSandbox() BoxID = %q, want %q", info.BoxID, name)
		}

		pod, getErr := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{})
		if getErr != nil {
			t.Fatalf("get recreated pod: %v", getErr)
		}
		container := pod.Spec.Containers[0]
		if len(container.Command) != 3 || container.Command[2] != "tail -f /dev/null" {
			t.Fatalf("pod command = %+v, want the plain tail -f /dev/null command", container.Command)
		}
	})
}

// TestK8sEnsureSandboxLaunchesJupyterEvenWhenPodFirstCreatedWithZeroProxyState
// is the regression test for the ordering bug the ProxyStateReader doc
// comment describes: WriteGuestDir/WriteGuestFile call EnsureSandbox with a
// zero-value ProxyState, and that call can be the one that creates the Pod
// (prepareFreshStartAgentEnvironment's guest-dir sync runs before
// StartSandboxVM ever supplies the real, persisted proxyState). Since a
// Pod's Command is immutable, if createPod trusted that zero-value
// parameter instead of resolveProxyState, jupyter would never launch.
func TestK8sEnsureSandboxLaunchesJupyterEvenWhenPodFirstCreatedWithZeroProxyState(t *testing.T) {
	r, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-jupyter-ordering")
	r.proxyStateReader = func(string) (ProxyState, error) {
		return ProxyState{Enabled: true, GuestPort: 8888, Token: "tok"}, nil
	}
	// Nothing in this fake cluster actually answers on the jupyter port, so
	// the readiness wait is expected to fail - keep it short so the test
	// doesn't hang. What's under test is what Command the Pod got, not
	// whether EnsureSandbox reports overall success. That failure also
	// deletes the Pod (see ensureSandboxResult), so capture its Command as
	// it's created rather than Get-ing it back afterward.
	r.config.JupyterReadyTimeout = 50 * time.Millisecond
	var createdCommand []string
	clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		pod, ok := createAction.GetObject().(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		createdCommand = append([]string(nil), pod.Spec.Containers[0].Command...)
		return false, nil, nil
	})

	if _, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{}); err == nil {
		t.Fatal("EnsureSandbox() error = nil, want a jupyter-readiness failure (nothing is listening in this fake cluster)")
	}

	if len(createdCommand) != 3 || !strings.Contains(createdCommand[2], "jupyterlab") {
		t.Fatalf("created pod command = %+v, want a jupyterlab launch command even though EnsureSandbox received ProxyState{}", createdCommand)
	}
}

func TestK8sJupyterProxyStateForPodSetsGuestHostFromPodIP(t *testing.T) {
	proxyState := ProxyState{GuestPort: 8888, Token: "tok", ProxyPath: "/agent-compose/session/sandbox-1"}
	pod := &corev1.Pod{Status: corev1.PodStatus{PodIP: "10.42.0.7"}}

	got := k8sJupyterProxyStateForPod(proxyState, pod)

	if !got.Enabled {
		t.Fatal("k8sJupyterProxyStateForPod() Enabled = false, want true")
	}
	if got.GuestHost != "10.42.0.7" {
		t.Fatalf("k8sJupyterProxyStateForPod() GuestHost = %q, want pod IP %q", got.GuestHost, "10.42.0.7")
	}
	if got.GuestPort != 8888 || got.Token != "tok" || got.ProxyPath != proxyState.ProxyPath {
		t.Fatalf("k8sJupyterProxyStateForPod() = %+v, want GuestPort/Token/ProxyPath carried through unchanged", got)
	}
}

func TestK8sPodVolumeSpecsMapsPVCMounts(t *testing.T) {
	r := &k8sRuntime{config: &appconfig.Config{}, defaultNamespace: "agent-compose"}
	sandbox := testSandbox(t, "sandbox-volume")
	sandbox.VolumeMounts = []SandboxVolumeMount{
		{ID: "mount-cache", Type: "volume", Source: "cache", Target: "/cache", Driver: RuntimeDriverK8s, HostPath: "agent-compose/claim-cache", ReadOnly: true},
		{ID: "mount-cache-2", Type: "volume", Source: "cache", Target: "/cache-copy", Driver: RuntimeDriverK8s, HostPath: "agent-compose/claim-cache"},
	}
	volumes, mounts, err := r.podVolumeSpecs(sandbox, VMState{})
	if err != nil {
		t.Fatalf("podVolumeSpecs() error = %v", err)
	}
	if len(volumes) != 1 || len(mounts) != 2 || volumes[0].PersistentVolumeClaim == nil || volumes[0].PersistentVolumeClaim.ClaimName != "claim-cache" {
		t.Fatalf("PVC volumes/mounts = %#v / %#v", volumes, mounts)
	}
	if mounts[0].MountPath != "/cache" || !mounts[0].ReadOnly || mounts[1].MountPath != "/cache-copy" || mounts[0].Name != mounts[1].Name {
		t.Fatalf("volume mounts = %#v", mounts)
	}
}

func TestK8sPodVolumeSpecsRejectsBindAndCrossNamespaceMounts(t *testing.T) {
	r := &k8sRuntime{config: &appconfig.Config{}, defaultNamespace: "agent-compose"}
	for name, mount := range map[string]SandboxVolumeMount{
		"bind":      {Type: "bind", Source: "/host", Target: "/data", HostPath: "/host"},
		"namespace": {Type: "volume", Source: "cache", Target: "/data", Driver: RuntimeDriverK8s, HostPath: "other/claim"},
		"partial workspace overlap": {
			Type: "volume", Source: "cache", Target: "/workspace/cache", Driver: RuntimeDriverK8s, HostPath: "agent-compose/claim-cache",
		},
	} {
		t.Run(name, func(t *testing.T) {
			sandbox := testSandbox(t, "sandbox-"+name)
			sandbox.VolumeMounts = []SandboxVolumeMount{mount}
			if _, _, err := r.podVolumeSpecs(sandbox, VMState{}); err == nil {
				t.Fatal("podVolumeSpecs() error = nil")
			}
		})
	}
}

func TestK8sPodVolumeSpecsAllowsWholeWorkspaceOrHomeAsAVolume(t *testing.T) {
	r := &k8sRuntime{config: &appconfig.Config{}, defaultNamespace: "agent-compose"}
	for name, target := range map[string]string{"workspace": "/workspace", "home": "/root"} {
		t.Run(name, func(t *testing.T) {
			sandbox := testSandbox(t, "sandbox-"+name)
			sandbox.VolumeMounts = []SandboxVolumeMount{
				{Type: "volume", Source: "data", Target: target, Driver: RuntimeDriverK8s, HostPath: "agent-compose/claim-data"},
			}
			if _, _, err := r.podVolumeSpecs(sandbox, VMState{}); err != nil {
				t.Fatalf("podVolumeSpecs() error = %v, want the whole %s root to be a valid mount target", err, name)
			}
		})
	}
}

func TestK8sValidateVolumeMountTarget(t *testing.T) {
	const workspace, home = "/workspace", "/root"
	cases := map[string]struct {
		target  string
		wantErr bool
	}{
		"exact workspace match":        {"/workspace", false},
		"exact home match":             {"/root", false},
		"unrelated path":               {"/data/models", false},
		"strict descendant":            {"/workspace/cache", true},
		"strict ancestor":              {"/", true},
		"descendant of home":           {"/root/.cache", true},
		"lookalike prefix, not nested": {"/workspace2", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := k8sValidateVolumeMountTarget(tc.target, workspace, home)
			if tc.wantErr && err == nil {
				t.Fatalf("k8sValidateVolumeMountTarget(%q) error = nil, want an error", tc.target)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("k8sValidateVolumeMountTarget(%q) error = %v, want nil", tc.target, err)
			}
		})
	}
}

func TestK8sEnsureSandboxSupportsFullLengthSandboxID(t *testing.T) {
	r, clientset := newTestK8sRuntime()
	sandbox := testSandbox(t, strings.Repeat("a", 64))

	info, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
	if err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	pod, err := clientset.CoreV1().Pods("default").Get(context.Background(), info.BoxID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected created pod to exist: %v", err)
	}
	labelValue := pod.Labels[k8sSandboxLabelID]
	if validationErrors := validation.IsLabelValue(labelValue); len(validationErrors) > 0 {
		t.Fatalf("pod label %s = %q is invalid: %v", k8sSandboxLabelID, labelValue, validationErrors)
	}
	if labelValue == sandbox.Summary.ID {
		t.Fatalf("pod label %s retained overlong sandbox ID %q", k8sSandboxLabelID, labelValue)
	}
	if pod.Annotations[k8sSandboxLabelID] != sandbox.Summary.ID {
		t.Fatalf("pod annotation %s = %q, want full sandbox ID %q", k8sSandboxLabelID, pod.Annotations[k8sSandboxLabelID], sandbox.Summary.ID)
	}
}

func TestK8sEnsureSandboxFindsExistingPod(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-2")
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-compose-sandbox-2",
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:     sandbox.Summary.ID,
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, clientset := newTestK8sRuntime(existing)

	info, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
	if err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	if info.BoxID != existing.Name {
		t.Fatalf("EnsureSandbox() BoxID = %q, want %q", info.BoxID, existing.Name)
	}

	pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("EnsureSandbox() on an existing pod created %d pods, want 1", len(pods.Items))
	}
}

// TestK8sEnsureSandboxSelfHealsWhenReusedPodsJupyterNeverBecomesReady is the
// regression test for the k8s-runtime PR's #9 finding: a Pod whose jupyter
// label already matches the requested state (so k8sPodNeedsRecreate finds
// nothing stale) but whose jupyter server never actually answers looks
// identical to a healthy Pod to every check EnsureSandbox has. Left in
// place, every future EnsureSandbox call for this sandbox (including the
// ones WriteGuestFile/WriteGuestDir trigger internally) would burn a full
// JupyterReadyTimeout and fail again, forever, with no way to self-heal
// short of a manual kubectl delete - this asserts the Pod is torn down
// instead, and that a subsequent call can actually make progress.
func TestK8sEnsureSandboxSelfHealsWhenReusedPodsJupyterNeverBecomesReady(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-jupyter-stuck")
	name := (&k8sRuntime{}).podName(sandbox, VMState{})
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:      sandbox.Summary.ID,
				k8sSandboxLabelDriver:  RuntimeDriverK8s,
				k8sSandboxLabelJupyter: "true",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, clientset := newTestK8sRuntime(existing)
	r.proxyStateReader = func(string) (ProxyState, error) {
		return ProxyState{Enabled: true, GuestPort: 8888, Token: "tok"}, nil
	}
	r.config.JupyterReadyTimeout = 50 * time.Millisecond

	if _, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{}); err == nil {
		t.Fatal("EnsureSandbox() error = nil, want a jupyter-readiness failure (nothing is listening in this fake cluster)")
	}
	if _, getErr := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(getErr) {
		t.Fatalf("get pod after jupyter-readiness failure: err = %v, want NotFound (a stuck Pod must not be left behind to fail every future call)", getErr)
	}

	// The self-heal only matters if the next call can actually make
	// progress rather than hanging on the same stuck Pod: it must create a
	// fresh one (which then also fails readiness and gets cleaned up in
	// turn, since this fake cluster still answers nothing - the point is
	// that a create happens at all, not that a Pod survives).
	createCount := 0
	clientset.PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		createCount++
		return false, nil, nil
	})
	if _, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{}); err == nil {
		t.Fatal("EnsureSandbox() (second call) error = nil, want the same jupyter-readiness failure (still nothing listening)")
	}
	if createCount != 1 {
		t.Fatalf("pod creates during second EnsureSandbox() = %d, want 1 (a fresh Pod, not a hang on the first call's cleaned-up Pod)", createCount)
	}
}

// TestK8sEnsureSandboxLeavesJupyterPodAloneWhenCallerContextIsDone is a
// regression test for a gap in the self-heal above: waitForJupyterProxy's
// readyCtx is derived from the caller's ctx, so a caller that cancels (or
// whose own deadline passes) fails that wait exactly the same way a
// genuine JupyterReadyTimeout expiry would. That failure says nothing
// about whether this Pod's jupyter would have come up given more time, so
// it must not be force-deleted for a timeout that was never its own.
func TestK8sEnsureSandboxLeavesJupyterPodAloneWhenCallerContextIsDone(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-jupyter-caller-cancelled")
	name := (&k8sRuntime{}).podName(sandbox, VMState{})
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:      sandbox.Summary.ID,
				k8sSandboxLabelDriver:  RuntimeDriverK8s,
				k8sSandboxLabelJupyter: "true",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, clientset := newTestK8sRuntime(existing)
	r.proxyStateReader = func(string) (ProxyState, error) {
		return ProxyState{Enabled: true, GuestPort: 8888, Token: "tok"}, nil
	}
	// Long enough that the caller's own context (below) is what gives out
	// first, not readyCtx's derived JupyterReadyTimeout.
	r.config.JupyterReadyTimeout = 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := r.EnsureSandbox(ctx, sandbox, VMState{}, ProxyState{}); err == nil {
		t.Fatal("EnsureSandbox() error = nil, want the caller's context to have run out")
	}

	if _, getErr := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{}); getErr != nil {
		t.Fatalf("get pod after caller-context cancellation: err = %v, want the Pod left alone (this failure was the caller's, not the Pod's)", getErr)
	}
}

// TestK8sEnsureSandboxWaitsForJupyterUnreadyPodToActuallyBeDeleted is a
// regression test for a second gap in the self-heal above: deleting the
// Pod isn't enough on its own - Kubernetes doesn't remove a Pod object the
// instant Delete returns (even with GracePeriodSeconds=0, see
// TestK8sEnsureSandboxWaitsForStalePodNameToFreeUpBeforeRecreating for the
// same race on the stale-recreate path above). Without waiting for it to
// actually be gone, an immediate retry could either find this same
// Terminating Pod again or have its own createPod call hit AlreadyExists
// on this Pod's deterministic name.
func TestK8sEnsureSandboxWaitsForJupyterUnreadyPodToActuallyBeDeleted(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-jupyter-delete-race")
	name := (&k8sRuntime{}).podName(sandbox, VMState{})
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:      sandbox.Summary.ID,
				k8sSandboxLabelDriver:  RuntimeDriverK8s,
				k8sSandboxLabelJupyter: "true",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, clientset := newTestK8sRuntime(existing)
	r.proxyStateReader = func(string) (ProxyState, error) {
		return ProxyState{Enabled: true, GuestPort: 8888, Token: "tok"}, nil
	}
	r.config.JupyterReadyTimeout = 50 * time.Millisecond

	var deleteCalled bool
	clientset.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deleteCalled = true
		return true, nil, nil // handled: swallow the delete, object stays in the tracker
	})
	var gets int
	var terminatedFromTracker bool
	clientset.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok || getAction.GetName() != name || terminatedFromTracker {
			return false, nil, nil
		}
		gets++
		if gets < 3 {
			return false, nil, nil // fall through to the tracker, which still has it
		}
		terminatedFromTracker = true
		_ = clientset.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "default", name)
		return false, nil, nil
	})

	if _, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{}); err == nil {
		t.Fatal("EnsureSandbox() error = nil, want a jupyter-readiness failure (nothing is listening in this fake cluster)")
	}
	if !deleteCalled {
		t.Fatal("Delete was never called on the jupyter-unready pod")
	}
	if gets < 3 {
		t.Fatalf("Get was called %d time(s), want at least 3 (EnsureSandbox must actually wait for the Pod to be gone, not return right after Delete returns)", gets)
	}
}

// TestK8sEnsureSandboxFinishesJupyterUnreadyCleanupAfterCallerContextExpires
// is the regression test for a review finding on the fix above: once
// ctx.Err() has already decided this is a genuine Pod-side jupyter failure
// (not the caller giving up), the cleanup itself must not still be at the
// mercy of ctx - waitForPodDeleted can poll for several seconds, and ctx
// expiring partway through that window must not abort the cleanup with the
// Pod still Terminating (which would reintroduce the exact stuck state
// this whole path exists to fix).
func TestK8sEnsureSandboxFinishesJupyterUnreadyCleanupAfterCallerContextExpires(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-jupyter-ctx-expires-during-cleanup")
	name := (&k8sRuntime{}).podName(sandbox, VMState{})
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:      sandbox.Summary.ID,
				k8sSandboxLabelDriver:  RuntimeDriverK8s,
				k8sSandboxLabelJupyter: "true",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, clientset := newTestK8sRuntime(existing)
	r.proxyStateReader = func(string) (ProxyState, error) {
		return ProxyState{Enabled: true, GuestPort: 8888, Token: "tok"}, nil
	}
	r.config.JupyterReadyTimeout = 50 * time.Millisecond

	// Stall waitForPodDeleted's confirmation for a few polls (200ms apart)
	// so there's a window after the jupyter-readiness failure (~50ms) in
	// which ctx below expires while cleanup is still in progress. ctx's
	// budget (200ms) leaves ~150ms of margin over JupyterReadyTimeout
	// rather than the ~25ms a tighter budget would - still comfortably
	// short of the ~250ms+ these polls take to actually finish, but not so
	// tight that scheduler jitter or -race overhead could flip which
	// timeout fires first and produce a failure unrelated to the code
	// under test.
	clientset.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil // handled: swallow the delete, object stays in the tracker
	})
	var gets int
	var verifying bool
	clientset.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok || getAction.GetName() != name || verifying {
			// verifying excludes this test's own final assertion below from
			// the count - otherwise that Get would itself be the 3rd hit
			// and trigger the tracker delete, making this test pass
			// regardless of whether EnsureSandbox's own cleanup ever
			// actually got there.
			return false, nil, nil
		}
		gets++
		if gets < 3 {
			return false, nil, nil // fall through to the tracker, which still has it
		}
		_ = clientset.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "default", name)
		return false, nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := r.EnsureSandbox(ctx, sandbox, VMState{}, ProxyState{}); err == nil {
		t.Fatal("EnsureSandbox() error = nil, want a jupyter-readiness failure (nothing is listening in this fake cluster)")
	}
	if ctx.Err() == nil {
		t.Fatal("test setup invalid: ctx did not actually expire during this call")
	}
	verifying = true
	if _, getErr := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(getErr) {
		t.Fatalf("get pod after ctx expired mid-cleanup: err = %v, want NotFound (cleanup must finish even once the caller's context runs out)", getErr)
	}
}

func TestK8sStopAndRemoveSandboxDeletePod(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-3")
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-compose-sandbox-3",
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:     sandbox.Summary.ID,
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, clientset := newTestK8sRuntime(existing)

	missing, err := r.StopSandbox(context.Background(), sandbox, VMState{})
	if err != nil {
		t.Fatalf("StopSandbox() error = %v", err)
	}
	if missing {
		t.Fatal("StopSandbox() reported missing for an existing pod")
	}
	if _, err := clientset.CoreV1().Pods("default").Get(context.Background(), existing.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("StopSandbox() did not delete the pod")
	}

	// RemoveSandbox (and a second StopSandbox) on an already-deleted pod must
	// tolerate the not-found case rather than erroring.
	if err := r.RemoveSandbox(context.Background(), sandbox, VMState{}); err != nil {
		t.Fatalf("RemoveSandbox() on an already-removed pod returned error: %v", err)
	}
	missing, err = r.StopSandbox(context.Background(), sandbox, VMState{})
	if err != nil {
		t.Fatalf("StopSandbox() on an already-removed pod returned error: %v", err)
	}
	if !missing {
		t.Fatal("StopSandbox() on an already-removed pod should report missing")
	}
}

func TestK8sClientDoesNotCacheBuildFailure(t *testing.T) {
	// A nonexistent ExplicitPath fails clientcmd's ClientConfig() call
	// immediately (no in-cluster fallback once an explicit path is set), so
	// this exercises client()'s error path without touching the network.
	r := &k8sRuntime{
		config:  &appconfig.Config{K8sKubeconfigPath: filepath.Join(t.TempDir(), "does-not-exist")},
		clients: map[string]*k8sClientEntry{},
	}

	if _, _, err := r.client(""); err == nil {
		t.Fatal("client() error = nil, want a load-config failure")
	}
	if len(r.clients) != 0 {
		t.Fatalf("client() cached a failed build: r.clients = %#v, want empty (a transient failure must not permanently poison this context for the rest of the daemon's process lifetime)", r.clients)
	}
	// A second call must retry from scratch, not return a cached error.
	if _, _, err := r.client(""); err == nil {
		t.Fatal("client() (second call) error = nil, want the same load-config failure retried, not a cached success")
	}
}

// concurrentK8sExecutor mimics what k8s.io/client-go/tools/remotecommand's
// real streamProtocolV2.stream does against a live apiserver: copyStdout
// and copyStderr each run in their own goroutine (see client-go
// tools/remotecommand/v2.go), unlike fakeK8sExecutor above, which writes
// stdout then stderr sequentially on a single goroutine and so never
// exercises this.
type concurrentK8sExecutor struct{}

func (concurrentK8sExecutor) Stream(options remotecommand.StreamOptions) error {
	return concurrentK8sExecutor{}.StreamWithContext(context.Background(), options)
}

func (concurrentK8sExecutor) StreamWithContext(_ context.Context, options remotecommand.StreamOptions) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = fmt.Fprintf(options.Stdout, "out-%d\n", i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = fmt.Fprintf(options.Stderr, "err-%d\n", i)
		}
	}()
	wg.Wait()
	return nil
}

// TestK8sExecConcurrentStdoutStderrRace is the regression test for the
// data race dockerExecCollector.mu fixes: this driver hands the same
// collector's Stdout/Stderr writers to remotecommand.StreamWithContext,
// whose real executor (unlike fakeK8sExecutor) writes both concurrently.
// Run with -race, this failed before the mu field existed.
func TestK8sExecConcurrentStdoutStderrRace(t *testing.T) {
	r, _ := newTestK8sRuntime()
	sandbox := testSandbox(t, "sandbox-race")
	if _, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	r.newExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
		return concurrentK8sExecutor{}, nil
	}

	if _, err := r.Exec(context.Background(), sandbox, VMState{}, ExecSpec{Command: "echo hi"}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
}

func TestK8sExecScriptFailsClosedWhenCwdDoesNotExist(t *testing.T) {
	script := k8sExecScript(ExecSpec{
		Command: "echo",
		Args:    []string{"should not run"},
		Env:     map[string]string{"SHOULD_NOT": "be-set"},
	}, filepath.Join(t.TempDir(), "does-not-exist"))

	output, err := exec.Command("sh", "-lc", script).CombinedOutput()
	if err == nil {
		t.Fatalf("script with a missing cwd succeeded and printed %q, want a failure from the bad cd", output)
	}
}

func TestK8sExecScriptPreservesNestedShellQuotes(t *testing.T) {
	script := k8sExecScript(ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", `printf '%s' "prompt's value"`},
	}, "")

	output, err := exec.Command("sh", "-lc", script).CombinedOutput()
	if err != nil {
		t.Fatalf("execute k8s script %q: %v: %s", script, err, output)
	}
	if got, want := string(output), "prompt's value"; got != want {
		t.Fatalf("k8s script output = %q, want %q", got, want)
	}
}

func TestK8sEnsureSandboxDeletesPodItCreatedWhenWaitFails(t *testing.T) {
	r, clientset := newTestK8sRuntimeNoAutoRunning()
	sandbox := testSandbox(t, "sandbox-wait-fail")

	_, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
	if err == nil {
		t.Fatal("EnsureSandbox() error = nil, want a wait-for-running failure")
	}

	pods, listErr := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if listErr != nil {
		t.Fatalf("list pods: %v", listErr)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("EnsureSandbox() left %d pod(s) behind after a failed create, want 0 (no zombie pods)", len(pods.Items))
	}
}

func TestK8sEnsureSandboxLeavesExistingPodOnWaitFailure(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-existing-wait-fail")
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-compose-sandbox-existing-wait-fail",
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:     sandbox.Summary.ID,
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	r, clientset := newTestK8sRuntimeNoAutoRunning(existing)

	_, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
	if err == nil {
		t.Fatal("EnsureSandbox() error = nil, want a wait-for-running failure")
	}

	if _, getErr := clientset.CoreV1().Pods("default").Get(context.Background(), existing.Name, metav1.GetOptions{}); getErr != nil {
		t.Fatalf("EnsureSandbox() deleted a pod it did not create: %v", getErr)
	}
}

func TestK8sEnsureSandboxRecreatesPodStuckInTerminalPhase(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-terminal-phase")
	name := (&k8sRuntime{}).podName(sandbox, VMState{})
	for _, phase := range []corev1.PodPhase{corev1.PodFailed, corev1.PodSucceeded} {
		t.Run(string(phase), func(t *testing.T) {
			existing := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
					Labels: map[string]string{
						k8sSandboxLabelID:     sandbox.Summary.ID,
						k8sSandboxLabelDriver: RuntimeDriverK8s,
					},
				},
				Status: corev1.PodStatus{Phase: phase},
			}
			r, clientset := newTestK8sRuntime(existing)

			info, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
			if err != nil {
				t.Fatalf("EnsureSandbox() error = %v, want the terminal pod replaced with a fresh one", err)
			}
			if info.BoxID != name {
				t.Fatalf("EnsureSandbox() BoxID = %q, want %q", info.BoxID, name)
			}

			pod, err := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get recreated pod: %v", err)
			}
			if pod.Status.Phase != corev1.PodRunning {
				t.Fatalf("recreated pod phase = %s, want Running (a %s pod must not be handed back as-is)", pod.Status.Phase, phase)
			}
		})
	}
}

// TestK8sWaitForPodDeletedPollsUntilGone is the regression test for the Pod
// name reuse race: Kubernetes doesn't remove a Pod object the instant
// Delete returns (even with GracePeriodSeconds=0), so a caller about to
// Create a new Pod under the same deterministic name has to actually wait
// for the old one to disappear first, not just fire-and-forget the delete.
func TestK8sWaitForPodDeletedPollsUntilGone(t *testing.T) {
	r, clientset := newTestK8sRuntime(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-compose-sandbox-deleting", Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})
	var gets int
	clientset.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < 3 {
			// Fall through to the default tracker, which still has the Pod
			// - simulates the object still being there (e.g. Terminating)
			// for the first couple of polls.
			return false, nil, nil
		}
		return true, nil, apierrors.NewNotFound(corev1.Resource("pods"), "agent-compose-sandbox-deleting")
	})

	if err := r.waitForPodDeleted(context.Background(), clientset, "default", "agent-compose-sandbox-deleting", 5*time.Second); err != nil {
		t.Fatalf("waitForPodDeleted() error = %v", err)
	}
	if gets < 3 {
		t.Fatalf("Get was called %d time(s), want at least 3 (must actually poll rather than trust the first check)", gets)
	}
}

func TestK8sWaitForPodDeletedTimesOutIfPodNeverGoesAway(t *testing.T) {
	r, clientset := newTestK8sRuntime(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-compose-sandbox-stuck", Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})

	err := r.waitForPodDeleted(context.Background(), clientset, "default", "agent-compose-sandbox-stuck", 100*time.Millisecond)
	if err == nil {
		t.Fatal("waitForPodDeleted() error = nil, want a timeout (the Pod never actually disappears in this test)")
	}
}

// TestK8sEnsureSandboxWaitsForStalePodNameToFreeUpBeforeRecreating is the
// integration-level counterpart to TestK8sWaitForPodDeleted*: those two
// only call waitForPodDeleted directly, which proves the helper itself
// works but not that EnsureSandbox actually calls it, in the right order
// relative to createPod. Every other EnsureSandbox-level recreate test
// uses a plain fake clientset, whose Delete() removes the object from the
// tracker synchronously - so those tests pass identically whether or not
// EnsureSandbox waits at all, and would not catch that call being dropped
// or reordered in a future refactor.
//
// This test instead makes Delete a no-op (the object stays in the tracker,
// simulating a real cluster's Terminating window) and only actually
// removes it from the tracker after a few polls - so if EnsureSandbox
// called createPod before waiting for that, the fake tracker's own name
// conflict check would produce a genuine AlreadyExists, the same way a
// real apiserver would.
func TestK8sEnsureSandboxWaitsForStalePodNameToFreeUpBeforeRecreating(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-recreate-race")
	name := (&k8sRuntime{}).podName(sandbox, VMState{})
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:     sandbox.Summary.ID,
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed},
	}
	r, clientset := newTestK8sRuntime(existing)

	var deleteCalled bool
	clientset.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deleteCalled = true
		return true, nil, nil // handled: swallow the delete, object stays in the tracker
	})
	var gets int
	var terminatedFromTracker bool
	clientset.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok || getAction.GetName() != name || terminatedFromTracker {
			// Once the stale Pod has actually been removed below, every
			// later Get (waitForPodDeleted's own confirmation, the new
			// Pod's readiness check, this test's final assertion) must
			// fall straight through to the tracker's real state - not run
			// this counting logic again and delete the *new* same-named
			// Pod out from under those later calls.
			return false, nil, nil
		}
		gets++
		if gets < 3 {
			return false, nil, nil // fall through to the tracker, which still has it
		}
		// "Finish" the termination on this poll, then fall through so this
		// and every later Get sees it gone via the tracker's own NotFound,
		// not a canned response.
		terminatedFromTracker = true
		_ = clientset.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "default", name)
		return false, nil, nil
	})

	info, err := r.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{})
	if err != nil {
		t.Fatalf("EnsureSandbox() error = %v, want the stale pod replaced once its name actually frees up", err)
	}
	if !deleteCalled {
		t.Fatal("Delete was never called on the stale pod")
	}
	if gets < 3 {
		t.Fatalf("Get was called %d time(s), want at least 3 (EnsureSandbox must actually wait for the name to free up, not create right after Delete returns)", gets)
	}
	if info.BoxID != name {
		t.Fatalf("EnsureSandbox() BoxID = %q, want %q", info.BoxID, name)
	}
	pod, getErr := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{})
	if getErr != nil {
		t.Fatalf("get recreated pod: %v", getErr)
	}
	if pod.Status.Phase != corev1.PodRunning {
		t.Fatalf("recreated pod phase = %s, want Running", pod.Status.Phase)
	}
}

func TestK8sWaitForPodRunningFailsFastOnImagePullBackOff(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-compose-sandbox-bad-image", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: k8sContainerName,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "rpc error: image not found"},
				},
			}},
		},
	}
	r, _ := newTestK8sRuntime(pod)
	r.config.SandboxStartTimeout = time.Minute

	start := time.Now()
	_, err := r.waitForPodRunning(context.Background(), r.clients[""].clientset, "default", pod.Name)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("waitForPodRunning() error = nil, want ImagePullBackOff failure")
	}
	if !strings.Contains(err.Error(), "ImagePullBackOff") || !strings.Contains(err.Error(), "image not found") {
		t.Fatalf("waitForPodRunning() error = %q, want it to name the stuck reason", err.Error())
	}
	if elapsed >= r.config.SandboxStartTimeout {
		t.Fatalf("waitForPodRunning() took %s, want it to fail fast well under the %s timeout", elapsed, r.config.SandboxStartTimeout)
	}
}

func TestK8sIsSandboxAlive(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-alive")
	running := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-compose-sandbox-alive",
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:     sandbox.Summary.ID,
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, _ := newTestK8sRuntime(running)

	alive, err := r.IsSandboxAlive(context.Background(), sandbox, VMState{})
	if err != nil {
		t.Fatalf("IsSandboxAlive() error = %v", err)
	}
	if !alive {
		t.Fatal("IsSandboxAlive() = false, want true for a Running pod")
	}

	missingSandbox := testSandbox(t, "sandbox-missing")
	alive, err = r.IsSandboxAlive(context.Background(), missingSandbox, VMState{})
	if err != nil {
		t.Fatalf("IsSandboxAlive() for a missing pod returned error = %v", err)
	}
	if alive {
		t.Fatal("IsSandboxAlive() = true, want false when the pod does not exist")
	}
}

func TestK8sReadGuestFilePreservesBinaryContent(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-binary")
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-compose-sandbox-binary",
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:     sandbox.Summary.ID,
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, _ := newTestK8sRuntime(existing)

	// A byte sequence that is not valid UTF-8 (lone continuation/invalid
	// lead bytes) - the streaming decoder used for stdout/stderr display
	// would replace these with U+FFFD, corrupting them. A real-world
	// equivalent is any binary artifact: a PNG, a zip, a compiled binary.
	binary := []byte{0x89, 0x50, 0x4E, 0x47, 0xFF, 0xD8, 0x00, 0x01, 0xC3, 0x28, 0xA0, 0xA1}
	r.newExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
		return fakeK8sExecutor{stdout: binary}, nil
	}

	got, err := r.ReadGuestFile(context.Background(), sandbox, VMState{}, "/workspace/output.bin")
	if err != nil {
		t.Fatalf("ReadGuestFile() error = %v", err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("ReadGuestFile() = %#v, want exact binary round-trip %#v", got, binary)
	}
}

func TestK8sReadGuestFileReportsNonZeroExitCode(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-exit-code")
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-compose-sandbox-exit-code",
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:     sandbox.Summary.ID,
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	r, _ := newTestK8sRuntime(existing)
	r.newExecutor = func(*rest.Config, string, *url.URL) (remotecommand.Executor, error) {
		return fakeK8sExecutor{stderr: []byte("cat: /workspace/missing: No such file or directory"), err: k8sexec.CodeExitError{Err: nil, Code: 1}}, nil
	}

	_, err := r.ReadGuestFile(context.Background(), sandbox, VMState{}, "/workspace/missing")
	if err == nil {
		t.Fatal("ReadGuestFile() error = nil, want a non-zero exit code error")
	}
	if !strings.Contains(err.Error(), "exit code 1") || !strings.Contains(err.Error(), "No such file or directory") {
		t.Fatalf("ReadGuestFile() error = %q, want it to surface the exit code and stderr", err.Error())
	}
}

// TestK8sWriteGuestFileDeletesRatherThanTruncatesOnNilContent is the
// regression test for the k8s-runtime PR's #2 finding: callers (see
// mcp_config.go, agent_files.go, runtime_config.go) pass content == nil to
// mean "this file should no longer exist" - the same signal that gets
// os.Remove on docker/boxlite's shared mount. A plain "cat > path" instead
// leaves a 0-byte file behind, which a guest-side JSON/TOML reader treats
// as invalid content (a parse error) rather than "nothing configured" (the
// ENOENT case such readers actually handle).
func TestK8sWriteGuestFileDeletesRatherThanTruncatesOnNilContent(t *testing.T) {
	sandbox := testSandbox(t, "sandbox-write-guest-file")
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-compose-sandbox-write-guest-file",
			Namespace: "default",
			Labels: map[string]string{
				k8sSandboxLabelID:     sandbox.Summary.ID,
				k8sSandboxLabelDriver: RuntimeDriverK8s,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	for _, tc := range []struct {
		name         string
		content      []byte
		wantContains string
		wantExcludes string
	}{
		{name: "nil content deletes", content: nil, wantContains: "rm -f", wantExcludes: "cat >"},
		{name: "non-nil content writes", content: []byte("{}"), wantContains: "cat >", wantExcludes: "rm -f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestK8sRuntime(existing)
			var gotScript string
			r.newExecutor = func(_ *rest.Config, _ string, u *url.URL) (remotecommand.Executor, error) {
				command := u.Query()["command"]
				if len(command) == 3 {
					gotScript = command[2]
				}
				return fakeK8sExecutor{}, nil
			}

			if err := r.WriteGuestFile(context.Background(), sandbox, VMState{}, "/data/state/agents/mcp/config.json", tc.content); err != nil {
				t.Fatalf("WriteGuestFile() error = %v", err)
			}
			if !strings.Contains(gotScript, tc.wantContains) {
				t.Fatalf("script = %q, want it to contain %q", gotScript, tc.wantContains)
			}
			if strings.Contains(gotScript, tc.wantExcludes) {
				t.Fatalf("script = %q, want it to not contain %q", gotScript, tc.wantExcludes)
			}
		})
	}
}

func TestK8sPodExecOptionsAttachStdinOnlyForInternalTransfers(t *testing.T) {
	command := "cat > /workspace/input.txt"
	withoutStdin := k8sPodExecOptions(command, false)
	if withoutStdin.Stdin {
		t.Fatal("ordinary k8s exec unexpectedly attaches stdin")
	}
	withStdin := k8sPodExecOptions(command, true)
	if !withStdin.Stdin || !withStdin.Stdout || !withStdin.Stderr {
		t.Fatalf("transfer exec options = %#v, want stdin/stdout/stderr attached", withStdin)
	}
	if got := strings.Join(withStdin.Command, " "); got != "sh -lc "+command {
		t.Fatalf("transfer exec command = %q", got)
	}
}
