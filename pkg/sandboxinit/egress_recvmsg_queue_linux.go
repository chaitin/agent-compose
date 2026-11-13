//go:build linux

package sandboxinit

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	dnsRecvmsgPendingLimit   = 1024
	dnsRecvmsgPollRetryDelay = 10 * time.Millisecond
)

// One worker per supervisor serializes receives across exec listeners. A single
// poll waits for socket readiness and the eventfd; cancellation is swept at
// most every 100ms, rather than issuing an ioctl and poll per entry every 5ms.
type dnsRecvmsgQueue struct {
	add    chan *dnsRecvmsg
	slots  chan struct{}
	stop   chan struct{}
	done   chan struct{}
	wake   int
	mu     sync.Mutex
	closed bool
}

func newDNSRecvmsgQueue() (*dnsRecvmsgQueue, error) {
	wake, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("create DNS receive wakeup: %w", err)
	}
	q := &dnsRecvmsgQueue{add: make(chan *dnsRecvmsg, dnsRecvmsgPendingLimit), slots: make(chan struct{}, dnsRecvmsgPendingLimit), stop: make(chan struct{}), done: make(chan struct{}), wake: wake}
	go q.run()
	return q, nil
}

func (q *dnsRecvmsgQueue) signal() {
	var value [8]byte
	binary.NativeEndian.PutUint64(value[:], 1)
	for {
		_, err := unix.Write(q.wake, value[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return // EAGAIN means a wakeup is already pending; the owned fd stays open.
	}
}

func (q *dnsRecvmsgQueue) submit(r *dnsRecvmsg) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		select {
		case q.slots <- struct{}{}:
			q.add <- r
			q.signal()
			return
		default:
		}
	}
	// EAGAIN keeps the receive retryable. ENOMEM makes some resolvers abandon
	// the query instead of polling the socket again.
	replyConnect(r.listenerFD, r.request.ID, unix.EAGAIN)
	r.close()
}

func (q *dnsRecvmsgQueue) close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.stop)
		q.signal()
	}
	q.mu.Unlock()
	<-q.done
}

func (q *dnsRecvmsgQueue) release(r *dnsRecvmsg) { r.close(); <-q.slots }

func (q *dnsRecvmsgQueue) run() {
	defer close(q.done)
	defer func() { _ = unix.Close(q.wake) }()
	var pending []*dnsRecvmsg
	saved := make(dnsRecvmsgPackets)
	defer func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		for _, r := range pending {
			replyConnect(r.listenerFD, r.request.ID, unix.EIO)
			q.release(r)
		}
		for {
			select {
			case r := <-q.add:
				replyConnect(r.listenerFD, r.request.ID, unix.EIO)
				q.release(r)
			default:
				goto drained
			}
		}
	drained:
		for _, p := range saved {
			_ = unix.Close(p.fd)
		}
	}()
	sweep := time.Now().Add(100 * time.Millisecond)
	for {
		select {
		case <-q.stop:
			return
		default:
		}
		// Bound intake per turn so new work cannot starve already readable sockets.
		for count := 0; count < 32; count++ {
			select {
			case r := <-q.add:
				pending = append(pending, r)
			default:
				count = 32
			}
		}
		polls := make([]unix.PollFd, len(pending)+1)
		polls[0] = unix.PollFd{Fd: int32(q.wake), Events: unix.POLLIN}
		wait := time.Until(sweep)
		for i, r := range pending {
			polls[i+1] = unix.PollFd{Fd: int32(r.fd), Events: unix.POLLIN}
			wait = min(wait, r.waitTime())
			if saved[r.inode] != nil {
				wait = 0
			}
		}
		if len(q.add) > 0 {
			wait = 0
		}
		timeout := max(0, int((wait+time.Millisecond-1)/time.Millisecond))
		if len(pending) == 0 && len(saved) == 0 && len(q.add) == 0 {
			timeout = -1
		}
		_, err := unix.Poll(polls, timeout)
		if err != nil {
			if !errors.Is(err, unix.EINTR) {
				// A transient poll failure must not permanently disable DNS
				// receives while the supervisor keeps serving notifications.
				// Keep ownership of pending requests and retry after a pause.
				time.Sleep(dnsRecvmsgPollRetryDelay)
			}
		}
		if polls[0].Revents != 0 {
			var value [8]byte
			_, _ = unix.Read(q.wake, value[:])
		}
		maintenance := !time.Now().Before(sweep)
		kept := pending[:0]
		for i, r := range pending {
			packet := saved[r.inode]
			if polls[i+1].Revents == 0 && packet == nil && !r.nonblocking {
				if maintenance && !notificationIDValid(r.listenerFD, r.request.ID) {
					q.release(r)
					continue
				}
				if r.expired() {
					replyConnect(r.listenerFD, r.request.ID, unix.EAGAIN)
					q.release(r)
					continue
				}
				kept = append(kept, r)
				continue
			}
			done := saved.receive(r)
			if done {
				q.release(r)
			} else {
				kept = append(kept, r)
			}
		}
		clear(pending[len(kept):])
		pending = kept
		if maintenance {
			for inode, p := range saved {
				if time.Now().After(p.expires) {
					_ = unix.Close(p.fd)
					delete(saved, inode)
				}
			}
			sweep = time.Now().Add(100 * time.Millisecond)
		}
	}
}

// Clamp before converting timeval to Duration so huge socket timeouts cannot
// overflow. The periodic bound is for cancellation checks, not receive expiry.
func (r *dnsRecvmsg) waitTime() time.Duration {
	if r.nonblocking || r.expired() {
		return 0
	}
	if r.timeout.Sec == 0 && r.timeout.Usec == 0 {
		return 100 * time.Millisecond
	}
	elapsed := time.Since(r.started)
	seconds := r.timeout.Sec - int64(elapsed/time.Second)
	if seconds > 1 {
		return 100 * time.Millisecond
	}
	return min(100*time.Millisecond, time.Duration(seconds)*time.Second+time.Duration(r.timeout.Usec)*time.Microsecond-elapsed%time.Second)
}
