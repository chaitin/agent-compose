//go:build linux

package sandboxinit

import (
	"time"

	"golang.org/x/sys/unix"
)

// One worker per listener owns pending DNS receives. Bound retained descriptors
// and layouts even if a workload starts many indefinitely blocking receives.
const dnsRecvmsgPendingLimit = 1024

type dnsRecvmsgQueue struct {
	add  chan *dnsRecvmsg
	stop chan struct{}
	done chan struct{}
}

func newDNSRecvmsgQueue(listenerFD int) *dnsRecvmsgQueue {
	q := &dnsRecvmsgQueue{add: make(chan *dnsRecvmsg), stop: make(chan struct{}), done: make(chan struct{})}
	go q.run(listenerFD)
	return q
}

func (q *dnsRecvmsgQueue) close() { close(q.stop); <-q.done }

func (q *dnsRecvmsgQueue) run(listenerFD int) {
	defer close(q.done)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var pending []*dnsRecvmsg
	defer func() {
		for _, r := range pending {
			_ = unix.Close(r.fd)
		}
	}()
	for {
		var tick <-chan time.Time
		if len(pending) > 0 {
			tick = ticker.C
		}
		select {
		case <-q.stop:
			return
		case r := <-q.add:
			if r.try(listenerFD) {
				_ = unix.Close(r.fd)
				continue
			}
			if len(pending) == dnsRecvmsgPendingLimit {
				replyConnect(listenerFD, r.request.ID, unix.ENOMEM)
				_ = unix.Close(r.fd)
			} else {
				pending = append(pending, r)
			}
		case <-tick:
			kept := pending[:0]
			for _, r := range pending {
				if r.try(listenerFD) {
					_ = unix.Close(r.fd)
				} else {
					kept = append(kept, r)
				}
			}
			clear(pending[len(kept):])
			pending = kept
		}
	}
}
