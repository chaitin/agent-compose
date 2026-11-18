//go:build k8scompose

package driver

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// k8sResourceRequirements maps a declared limit onto the sandbox container.
// Requests equal limits, so the scheduler reserves exactly what the sandbox may
// use and the Pod lands in the Guaranteed QoS class for every declared
// dimension. An undeclared limit returns the zero value, which leaves the Pod
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
