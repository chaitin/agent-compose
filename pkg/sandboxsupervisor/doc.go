// Package sandboxsupervisor implements the per-sandbox egress relay that runs
// outside the workload container.
//
// A supervisor owns one Unix domain socket. The sandbox init process, which is
// the only trusted process inside the workload container, connects to that
// socket after duplicating a workload socket to a loopback relay. The
// supervisor authenticates the call with a per-sandbox random token, reads the
// destination and attribution handshake, and either opens the destination
// directly (engine-owned endpoints only) or performs a CONNECT against the
// daemon's egress proxy and relays bytes. The workload container itself can be
// created with NetworkMode=none: it keeps loopback for the init DNS stub and
// the relay, and reaches the outside world only through this socket.
//
// Policy is not decided here for ordinary egress. The daemon's forward proxy
// remains the single policy decision point; the supervisor preserves that
// property by never forwarding a destination the proxy has not accepted and by
// keeping the proxy credential out of the workload container.
package sandboxsupervisor
