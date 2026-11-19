//go:build k8scompose

package driver

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func k8sPodWithResources(resources corev1.ResourceRequirements) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "sandboxes", Name: "pod-1"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: k8sContainerName, Resources: resources}}},
	}
}

func TestRequireReusedK8sPodResourceLimits(t *testing.T) {
	limits := &SandboxResourceLimits{CPUMillis: 500, MemoryMiB: 1024}
	if err := requireReusedK8sPodResourceLimits(k8sPodWithResources(k8sResourceRequirements(limits)), limits); err != nil {
		t.Fatalf("reusing a Pod with the declared limits = %v, want reused", err)
	}
	if err := requireReusedK8sPodResourceLimits(k8sPodWithResources(corev1.ResourceRequirements{}), limits); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("reusing an unlimited Pod = %v, want a refusal", err)
	}
	// A request below the limit is not the requests=limits shape the driver
	// creates, for example a LimitRange default the declaration predates.
	burstable := k8sResourceRequirements(limits)
	burstable.Requests = nil
	if err := requireReusedK8sPodResourceLimits(k8sPodWithResources(burstable), limits); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("reusing a Pod whose requests differ = %v, want a refusal", err)
	}
	if err := requireReusedK8sPodResourceLimits(k8sPodWithResources(corev1.ResourceRequirements{}), nil); err != nil {
		t.Fatalf("undeclared sandbox = %v, want any Pod reused", err)
	}
}
