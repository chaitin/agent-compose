// Package sandboxinit owns the stage-1 sandbox init process and its control
// protocol.
//
// The init process is PID 1 inside a Docker sandbox. It starts and supervises
// every workload process, reaps orphaned descendants, forwards termination,
// and exposes a small Unix-socket control API so the daemon can start commands
// without Docker exec. The control API is deliberately independent of the
// driver: the Docker driver is the only caller today, but the protocol also
// leaves room for a future seccomp/Landlock hook in the process launch path.
//
// The package is split into a portable protocol/client half and a Linux-only
// server half. The daemon only links the client; the init binary links the
// server.
package sandboxinit
