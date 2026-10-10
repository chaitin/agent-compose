package driver

// securityContextObserved carries the per-dimension evidence string for the
// four security-context dimensions.
type securityContextObserved struct {
	CapabilityDrop string
	ReadOnlyRootfs string
	NonRootUser    string
	UserNamespaces string
}

// securityContextReasons carries the not-enforced reason for each
// security-context dimension. A driver can expose an addressable surface for
// one dimension while it exposes none for another, so the reason is tracked per
// dimension instead of being assumed uniform across the driver.
type securityContextReasons struct {
	CapabilityDrop string
	ReadOnlyRootfs string
	NonRootUser    string
	UserNamespaces string
}

// uniformSecurityContextReasons applies one reason to all four security-context
// dimensions, which is the shape a driver has when its runtime configuration
// object exposes every security field or none of them.
func uniformSecurityContextReasons(reason string) securityContextReasons {
	return securityContextReasons{
		CapabilityDrop: reason,
		ReadOnlyRootfs: reason,
		NonRootUser:    reason,
		UserNamespaces: reason,
	}
}

// securityContextFacts builds the four security-context dimension
// declarations from their per-dimension reasons and evidence.
func securityContextFacts(reasons securityContextReasons, observed securityContextObserved) []RuntimeCapabilityDimensionFacts {
	return []RuntimeCapabilityDimensionFacts{
		{
			Dimension:       dimensionCapabilityDrop,
			Mechanism:       reasons.CapabilityDrop,
			Observed:        observed.CapabilityDrop,
			DefaultBehavior: "the workload keeps the image's default Linux capability set",
		},
		{
			Dimension:       dimensionReadOnlyRootfs,
			Mechanism:       reasons.ReadOnlyRootfs,
			Observed:        observed.ReadOnlyRootfs,
			DefaultBehavior: "the sandbox root filesystem stays writable for the workload",
		},
		{
			Dimension:       dimensionNonRootUser,
			Mechanism:       reasons.NonRootUser,
			Observed:        observed.NonRootUser,
			DefaultBehavior: "the workload runs as the image's default user",
		},
		{
			Dimension:       dimensionUserNamespaces,
			Mechanism:       reasons.UserNamespaces,
			Observed:        observed.UserNamespaces,
			DefaultBehavior: "no user-namespace remapping is applied to the workload",
		},
	}
}
