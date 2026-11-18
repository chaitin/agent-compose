//go:build k8scompose

package driver

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// k8sResourceRequirements maps a declared limit onto the sandbox container.
// Requests equal limits, so the scheduler reserves exactly what the sandbox may
// use. QoS is a Pod-level class: the Pod is Guaranteed only when both cpus and
// memory are declared, and Burstable when one is. An undeclared limit returns
// the zero value, which leaves the Pod
// exactly as it was before policy.resources existed (a namespace LimitRange can
// still default it, as it always could).
func k8sResourceRequirements(limits *SandboxResourceLimits) corev1.ResourceRequirements {
	if !limits.Declared() {
		return corev1.ResourceRequirements{}
	}
	values := corev1.ResourceList{}
	if limits.CPUMillis > 0 {
		values[corev1.ResourceCPU] = *resource.NewMilliQuantity(limits.CPUMillis, resource.DecimalSI)
	}
	if limits.MemoryMiB > 0 {
		values[corev1.ResourceMemory] = *resource.NewQuantity(int64(limits.MemoryMiB)*mib, resource.BinarySI)
	}
	return corev1.ResourceRequirements{Requests: values.DeepCopy(), Limits: values}
}

// k8sPodResourceLimits reads the limit the Pod's sandbox container carries.
// A dimension whose request differs from its limit is not the declared
// requests=limits shape, so it reads as unlimited.
func k8sPodResourceLimits(pod *corev1.Pod) SandboxResourceLimits {
	if pod == nil {
		return SandboxResourceLimits{}
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != k8sContainerName {
			continue
		}
		var actual SandboxResourceLimits
		limits, requests := container.Resources.Limits, container.Resources.Requests
		if cpu, ok := limits[corev1.ResourceCPU]; ok {
			if request, ok := requests[corev1.ResourceCPU]; ok && request.Cmp(cpu) == 0 {
				actual.CPUMillis = cpu.MilliValue()
			}
		}
		if memory, ok := limits[corev1.ResourceMemory]; ok {
			if request, ok := requests[corev1.ResourceMemory]; ok && request.Cmp(memory) == 0 && memory.Value()%mib == 0 {
				actual.MemoryMiB = uint32(memory.Value() / mib)
			}
		}
		return actual
	}
	return SandboxResourceLimits{}
}

// requireReusedK8sPodResourceLimits refuses to reuse a Pod whose limit differs
// from the declaration (see requireReusedRuntimeResourceLimits). A Pod's
// resources are immutable, so the only way to apply a new declaration is a new
// Pod.
func requireReusedK8sPodResourceLimits(pod *corev1.Pod, limits *SandboxResourceLimits) error {
	name := ""
	if pod != nil {
		name = pod.Namespace + "/" + pod.Name
	}
	return requireReusedRuntimeResourceLimits(fmt.Sprintf("k8s pod %s", name), limits, k8sPodResourceLimits(pod))
}
