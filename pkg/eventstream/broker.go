package eventstream

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	defaultMaxSubscribers   = 32
	defaultSubscriberBuffer = 512
	defaultReplayEvents     = 4096
	defaultIdleTimeout      = 5 * time.Minute
	defaultHeartbeat        = 15 * time.Second
	defaultMaxBatchEvents   = 128
)

var (
	// ErrDisabled reports that the daemon did not enable the event stream.
	ErrDisabled = errors.New("event stream is disabled")
	// ErrClosed reports that the daemon is shutting the broker down.
	ErrClosed = errors.New("event stream is closed")
	// ErrTooManySubscribers reports the configured concurrent subscription cap.
	ErrTooManySubscribers = errors.New("event stream subscriber limit reached")
)

// Config bounds the broker and the transport that serves it. The daemon's
// config package validates the environment-derived values; the zero value
// here means "use the default" so tests and callers can set only what they
// care about.
type Config struct {
	Enabled          bool
	MaxSubscribers   int
	SubscriberBuffer int
	ReplayEvents     int
	IdleTimeout      time.Duration
	Heartbeat        time.Duration
	MaxBatchEvents   int
}

// DefaultConfig returns the daemon defaults. The feature is disabled.
func DefaultConfig() Config {
	return Config{
		Enabled:          false,
		MaxSubscribers:   defaultMaxSubscribers,
		SubscriberBuffer: defaultSubscriberBuffer,
		ReplayEvents:     defaultReplayEvents,
		IdleTimeout:      defaultIdleTimeout,
		Heartbeat:        defaultHeartbeat,
		MaxBatchEvents:   defaultMaxBatchEvents,
	}
}

func (c Config) normalized() Config {
	defaults := DefaultConfig()
	if c.MaxSubscribers <= 0 {
		c.MaxSubscribers = defaults.MaxSubscribers
	}
	if c.SubscriberBuffer <= 0 {
		c.SubscriberBuffer = defaults.SubscriberBuffer
	}
	if c.ReplayEvents < 0 {
		c.ReplayEvents = 0
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaults.IdleTimeout
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = defaults.Heartbeat
	}
	if c.MaxBatchEvents <= 0 {
		c.MaxBatchEvents = defaults.MaxBatchEvents
	}
	return c
}

// Broker fans immutable events out to at most MaxSubscribers subscribers. It
// is inactive until the first subscription, and it returns to inactive (and
// clears its replay ring) after the last subscription has been gone for
// IdleTimeout. Publishing is non-blocking: a slow subscriber loses events and
// learns about it through TakeDropped.
type Broker struct {
	cfg Config

	epoch     string
	active    atomic.Bool
	seq       uint64
	subs      map[*Subscription]struct{}
	ring      *ringBuffer
	idleTimer *time.Timer
	lastUnsub time.Time
	closed    bool
	now       func() time.Time
	newEpoch  func() string

	mu sync.Mutex
}

func NewBroker(cfg Config) *Broker {
	cfg = cfg.normalized()
	return &Broker{
		cfg:      cfg,
		subs:     make(map[*Subscription]struct{}),
		now:      func() time.Time { return time.Now().UTC() },
		newEpoch: uuid.NewString,
	}
}

// Enabled reports the deployment switch, not whether a subscriber is active.
func (b *Broker) Enabled() bool {
	return b != nil && b.cfg.Enabled
}

// Config returns the effective normalized configuration.
func (b *Broker) Config() Config {
	if b == nil {
		return DefaultConfig()
	}
	return b.cfg
}

// Active reports whether the first subscription has activated the broker and
// the idle reclaim has not run yet.
func (b *Broker) Active() bool {
	return b != nil && b.active.Load()
}

// SubscriberCount returns the current number of live subscriptions.
func (b *Broker) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Subscribe validates activation and registers a filtered subscription. The
// replay slice and the live channel are produced under one lock, so an event
// cannot be both replayed and delivered live.
func (b *Broker) Subscribe(filter Filter, cursorToken string) (*Subscription, SubscribeResult, error) {
	if b == nil || !b.cfg.Enabled {
		return nil, SubscribeResult{}, ErrDisabled
	}
	var cursor Cursor
	hasCursor := false
	if cursorToken != "" {
		parsed, err := DecodeCursor(cursorToken)
		if err != nil {
			return nil, SubscribeResult{}, err
		}
		cursor, hasCursor = parsed, true
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, SubscribeResult{}, ErrClosed
	}
	if len(b.subs) >= b.cfg.MaxSubscribers {
		return nil, SubscribeResult{}, ErrTooManySubscribers
	}
	if !b.active.Load() {
		b.activateLocked()
	}
	sub := &Subscription{
		broker: b,
		filter: filter,
		ch:     make(chan *Event, b.cfg.SubscriberBuffer),
	}
	result := b.subscribeResultLocked(cursor, hasCursor, filter)
	b.subs[sub] = struct{}{}
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}
	b.lastUnsub = time.Time{}
	result.Subscription = sub
	return sub, result, nil
}

// SubscribeResult describes how the stream starts for one subscriber.
type SubscribeResult struct {
	Subscription *Subscription
	Epoch        string
	HeadCursor   string
	ReplayFloor  string
	SyncRequired bool
	Reason       string
	Replay       []*Event
}

func (b *Broker) subscribeResultLocked(cursor Cursor, hasCursor bool, filter Filter) SubscribeResult {
	head := b.seq
	result := SubscribeResult{
		Epoch:       b.epoch,
		HeadCursor:  EncodeCursor(b.epoch, head),
		ReplayFloor: EncodeCursor(b.epoch, b.replayFloorLocked()),
	}
	if !hasCursor {
		return result
	}
	if cursor.Epoch != b.epoch {
		result.SyncRequired = true
		result.Reason = "epoch_changed"
		return result
	}
	if cursor.Sequence > head {
		result.SyncRequired = true
		result.Reason = "cursor_ahead"
		return result
	}
	if !b.ring.canReplay(cursor.Sequence, head) {
		result.SyncRequired = true
		result.Reason = "cursor_expired"
		return result
	}
	result.Replay = b.ring.since(cursor.Sequence, filter)
	return result
}

func (b *Broker) activateLocked() {
	if b.epoch == "" {
		b.epoch = b.newEpoch()
	}
	b.seq = 0
	b.ring = newRingBuffer(b.cfg.ReplayEvents)
	b.lastUnsub = time.Time{}
	b.active.Store(true)
}

func (b *Broker) deactivateLocked() {
	b.active.Store(false)
	b.epoch = ""
	b.seq = 0
	b.ring = nil
	b.lastUnsub = time.Time{}
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}
}

// Publish assigns sequence, identity, and cursor, then fans out without ever
// blocking. It reports whether at least one subscriber accepted the event; a
// ringable event is still retained for replay when no subscriber is connected.
// Log events are assigned a sequence but never enter the replay ring.
func (b *Broker) Publish(event *Event) bool {
	if b == nil || !b.cfg.Enabled || event == nil || event.Type == "" || !b.active.Load() {
		return false
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = b.now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !b.active.Load() {
		return false
	}
	ringable := !IsLogType(event.Type)
	if !ringable && len(b.subs) == 0 {
		return false
	}
	b.seq++
	event.Sequence = b.seq
	event.ID = b.epoch + ":" + strconv.FormatUint(b.seq, 10)
	event.Cursor = EncodeCursor(b.epoch, b.seq)
	if ringable && b.ring != nil {
		b.ring.add(event)
	}
	delivered := false
	for sub := range b.subs {
		if !sub.filter.Matches(event) {
			continue
		}
		select {
		case sub.ch <- event:
			delivered = true
		default:
			sub.recordDrop(event)
		}
	}
	return delivered
}

// Head returns the current epoch, head cursor, and replay floor. An inactive
// broker returns empty strings.
func (b *Broker) Head() (epoch, headCursor, replayFloor string) {
	if b == nil {
		return "", "", ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active.Load() {
		return "", "", ""
	}
	return b.epoch, EncodeCursor(b.epoch, b.seq), EncodeCursor(b.epoch, b.replayFloorLocked())
}

func (b *Broker) replayFloorLocked() uint64 {
	if b.ring == nil {
		return b.seq + 1
	}
	if oldest, ok := b.ring.oldestSequence(); ok {
		return oldest
	}
	return b.seq + 1
}

func (b *Broker) unsubscribe(sub *Subscription) {
	if b == nil || sub == nil {
		return
	}
	b.mu.Lock()
	if _, ok := b.subs[sub]; ok {
		delete(b.subs, sub)
		if len(b.subs) == 0 {
			b.lastUnsub = b.now()
			b.scheduleIdleLocked()
		}
	}
	b.mu.Unlock()
	sub.closeChannel()
}

func (b *Broker) scheduleIdleLocked() {
	if b.cfg.IdleTimeout <= 0 || b.idleTimer != nil {
		return
	}
	b.idleTimer = time.AfterFunc(b.cfg.IdleTimeout, func() {
		b.deactivateIfIdleAt(b.now())
	})
}

// deactivateIfIdleAt is the timer body and a deterministic test seam.
func (b *Broker) deactivateIfIdleAt(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !b.active.Load() || len(b.subs) > 0 || b.lastUnsub.IsZero() {
		return false
	}
	if now.Sub(b.lastUnsub) < b.cfg.IdleTimeout {
		return false
	}
	b.deactivateLocked()
	return true
}

// Close releases every subscription and stops the idle timer. It is safe to
// call more than once.
func (b *Broker) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.active.Store(false)
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}
	subs := make([]*Subscription, 0, len(b.subs))
	for sub := range b.subs {
		subs = append(subs, sub)
	}
	b.subs = make(map[*Subscription]struct{})
	b.ring = nil
	b.mu.Unlock()
	for _, sub := range subs {
		sub.closeChannel()
	}
}

// Subscription is one client's filtered live queue.
type Subscription struct {
	broker *Broker
	filter Filter
	ch     chan *Event

	closeOnce sync.Once
	dropMu    sync.Mutex
	dropped   uint64
	firstDrop string
	lastDrop  string
}

// Events returns the live event channel. It is closed when the subscription
// or broker closes.
func (s *Subscription) Events() <-chan *Event {
	if s == nil {
		return nil
	}
	return s.ch
}

// Close unregisters the subscription. It is idempotent.
func (s *Subscription) Close() {
	if s == nil {
		return
	}
	s.broker.unsubscribe(s)
}

func (s *Subscription) closeChannel() {
	s.closeOnce.Do(func() {
		close(s.ch)
	})
}

// TakeDropped reports and resets events dropped because this subscriber's
// queue was full. The handler emits a GAP control frame with the returned
// cursor range.
func (s *Subscription) TakeDropped() (count uint64, firstCursor, lastCursor string) {
	if s == nil {
		return 0, "", ""
	}
	s.dropMu.Lock()
	defer s.dropMu.Unlock()
	count, firstCursor, lastCursor = s.dropped, s.firstDrop, s.lastDrop
	s.dropped, s.firstDrop, s.lastDrop = 0, "", ""
	return count, firstCursor, lastCursor
}

func (s *Subscription) recordDrop(event *Event) {
	s.dropMu.Lock()
	defer s.dropMu.Unlock()
	s.dropped++
	if s.firstDrop == "" {
		s.firstDrop = event.Cursor
	}
	s.lastDrop = event.Cursor
}

// ringBuffer is a fixed-capacity circular buffer of replayable events.
type ringBuffer struct {
	items []*Event
	start int
	size  int
}

func newRingBuffer(capacity int) *ringBuffer {
	if capacity <= 0 {
		return nil
	}
	return &ringBuffer{items: make([]*Event, capacity)}
}

func (r *ringBuffer) add(event *Event) {
	if r == nil || len(r.items) == 0 || event == nil {
		return
	}
	if r.size < len(r.items) {
		r.items[(r.start+r.size)%len(r.items)] = event
		r.size++
		return
	}
	r.items[r.start] = event
	r.start = (r.start + 1) % len(r.items)
}

func (r *ringBuffer) oldestSequence() (uint64, bool) {
	if r == nil || r.size == 0 {
		return 0, false
	}
	return r.items[r.start].Sequence, true
}

// canReplay reports whether a cursor is new enough that every control event it
// has not seen is still in the ring. A cursor equal to head is always valid;
// with an empty ring only the head is valid.
func (r *ringBuffer) canReplay(sequence, head uint64) bool {
	if sequence == head {
		return true
	}
	oldest, ok := r.oldestSequence()
	if !ok {
		return false
	}
	return sequence+1 >= oldest
}

func (r *ringBuffer) since(sequence uint64, filter Filter) []*Event {
	if r == nil || r.size == 0 {
		return nil
	}
	events := make([]*Event, 0, r.size)
	for i := 0; i < r.size; i++ {
		event := r.items[(r.start+i)%len(r.items)]
		if event.Sequence > sequence && filter.Matches(event) {
			events = append(events, event)
		}
	}
	return events
}
