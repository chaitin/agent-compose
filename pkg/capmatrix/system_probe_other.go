//go:build !linux

package capmatrix

// probeSystemCapabilities is the non-Linux implementation. The macOS binary
// builds with the docker driver and talks to a Linux host, so the host
// mechanism questions cannot be answered by probing this process's own kernel.
// The engine reports every system dimension as unsupported with the reason,
// rather than guessing or silently omitting it.
func probeSystemCapabilities() ([]ObservedCapability, error) {
	out := make([]ObservedCapability, 0, len(SystemDimensions()))
	for _, dimension := range SystemDimensions() {
		out = append(out, ObservedCapability{
			Dimension: dimension,
			State:     StateUnsupported,
			Mechanism: ReasonUnsupported,
			Observed:  "host mechanism probing is implemented for Linux only; this binary cannot probe the sandbox host's kernel",
			Missing:   "build and run the daemon on the Linux host whose kernel will host the sandbox",
			Source:    SourceMeasured,
		})
	}
	return out, nil
}
