package uss

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
)

// mtuConfig describes the packetization layer path MTU discovery (RFC 8899)
// performed by the client.
//
// The values are sizes of the whole encrypted datagram (UDP payload), because
// that is what the network has to carry. Callers subtract the protocol
// overhead to obtain the usable payload size.
type mtuConfig struct {
	enabled       bool
	base          int
	max           int
	step          int
	probeInterval time.Duration
	probeTimeout  time.Duration
	probeAttempts int
}

// probeStartDelay keeps the first probe from racing with session setup: a probe
// that is lost because the server has not started yet would waste a full search
// round.
const probeStartDelay = time.Second

// maxStaleProbeAcks bounds how many acknowledgements of abandoned probes are
// consumed before the round is given up, so a peer can not keep one probe
// running forever by answering old ids.
const maxStaleProbeAcks = 16

type probeOutcome uint8

const (
	// probeTooBig means the datagram was refused or swallowed, which is what a
	// path with a smaller mtu looks like.
	probeTooBig probeOutcome = iota
	// probeFits means the peer acknowledged it.
	probeFits
	// probeUnknown means the measurement itself failed, so the current estimate
	// must be left alone.
	probeUnknown
)

// kcpMTUBudget converts a datagram budget into the segment budget the ARQ layer
// may use, reserving room for our own envelope.
//
// The floor matters: kcp-go derives its segment size from this value, and a
// negative or tiny budget makes its flush loop compute a nonsense (unsigned)
// size and emit a huge segment. Both directions must go through here so the
// client and the server can not drift apart.
func kcpMTUBudget(datagramSize int) int {
	mtu := datagramSize - cleartextSize - aeadTagSize - 1
	if mtu < minKCPMTU {
		mtu = minKCPMTU
	}
	return mtu
}

func (c mtuConfig) validate() error {
	if c.base < 576 {
		return E.New("mtu base must be at least 576")
	}
	if c.max > maxDatagramSize-cleartextSize-aeadTagSize {
		return E.New("mtu max must be at most ", maxDatagramSize-cleartextSize-aeadTagSize)
	}
	if c.base > c.max {
		return E.New("mtu base must not be larger than mtu max")
	}
	if c.step < 1 {
		return E.New("mtu step must be positive")
	}
	if c.max > c.base && c.step > c.max-c.base {
		return E.New("mtu step must not be larger than the search range")
	}
	if c.probeTimeout <= 0 {
		return E.New("mtu probe_timeout must be positive")
	}
	if c.probeAttempts < 1 {
		return E.New("mtu probe_attempts must be positive")
	}
	if c.probeInterval <= 0 {
		return E.New("mtu probe_interval must be positive")
	}
	return nil
}

// mtuProber searches for the largest datagram size that traverses the path
// without being fragmented. It never relies on ICMP: every probe is an ordinary
// authenticated packet padded to the size under test, and success is only
// assumed when the peer acknowledges it.
type mtuProber struct {
	config     mtuConfig
	logger     logger.ContextLogger
	sendProbe  func(probeID uint32, size int) error
	onIncrease func(size int)

	current  atomic.Int64
	probeSeq atomic.Uint32
	acks     chan uint32

	access sync.Mutex
	lower  int
	upper  int
}

func newMTUProber(config mtuConfig, logger logger.ContextLogger, sendProbe func(probeID uint32, size int) error, onIncrease func(size int)) *mtuProber {
	prober := &mtuProber{
		config:     config,
		logger:     logger,
		sendProbe:  sendProbe,
		onIncrease: onIncrease,
		acks:       make(chan uint32, 64),
		lower:      config.base,
		upper:      config.max + config.step,
	}
	prober.current.Store(int64(config.base))
	return prober
}

// currentSize returns the largest datagram size currently believed to be safe.
func (p *mtuProber) currentSize() int {
	return int(p.current.Load())
}

// handleAck feeds a probe acknowledgement from the read path. When the queue is
// full the oldest entry is dropped rather than the newest, so a flood of stale
// acknowledgements can not hide the answer to the probe that is running now.
func (p *mtuProber) handleAck(probeID uint32) {
	select {
	case p.acks <- probeID:
		return
	default:
	}
	select {
	case <-p.acks:
	default:
	}
	select {
	case p.acks <- probeID:
	default:
	}
}

// observeReceived records the size of a datagram that actually arrived. Passive
// observation is how the server side follows the path without probing itself:
// anything the client managed to send also fits on the way back.
func (p *mtuProber) observeReceived(size int) {
	if size <= 0 {
		return
	}
	if size > p.config.max {
		size = p.config.max
	}
	for {
		previous := p.currentSize()
		if size <= previous {
			return
		}
		if p.current.CompareAndSwap(int64(previous), int64(size)) {
			p.notify(size)
			return
		}
	}
}

func (p *mtuProber) notify(size int) {
	if p.onIncrease != nil {
		p.onIncrease(size)
	}
}

func (p *mtuProber) run(ctx context.Context) {
	if !p.config.enabled {
		return
	}
	if !sleepContext(ctx, probeStartDelay) {
		return
	}
	for {
		p.search(ctx)
		if ctx.Err() != nil {
			return
		}
		if !sleepContext(ctx, p.config.probeInterval) {
			return
		}
	}
}

// search walks the whole range once. The bounds are reset every round so a
// route change that lowers the path MTU is detected instead of being masked by
// a previously learned (now stale) value.
func (p *mtuProber) search(ctx context.Context) {
	initial := p.currentSize()
	p.access.Lock()
	p.lower = p.config.base
	p.upper = p.config.max + p.config.step
	p.access.Unlock()
	for {
		size := p.nextProbeSize()
		if size <= 0 {
			break
		}
		switch p.probe(ctx, size) {
		case probeFits:
			p.record(size, true)
		case probeTooBig:
			p.record(size, false)
		case probeUnknown:
			// A transient failure says nothing about this size: keep the
			// current estimate and try again next round.
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
	p.access.Lock()
	settled := p.lower
	p.access.Unlock()
	if settled != p.currentSize() {
		// Shrinking is applied here rather than per probe, so a single lost
		// probe can not stall transfers mid round.
		p.current.Store(int64(settled))
		p.notify(settled)
	}
	if settled != initial {
		p.logger.Debug("uss: path mtu settled at ", settled, " (was ", initial, ")")
	}
}

func (p *mtuProber) nextProbeSize() int {
	p.access.Lock()
	defer p.access.Unlock()
	if p.upper-p.lower <= p.config.step {
		return 0
	}
	mid := p.lower + ((p.upper-p.lower)/2/p.config.step)*p.config.step
	if mid <= p.lower {
		mid = p.lower + p.config.step
	}
	// The configured maximum is a hard ceiling: a probe larger than it would
	// test a size we are never going to use.
	if mid > p.config.max {
		mid = p.config.max
	}
	if mid <= p.lower || mid >= p.upper {
		return 0
	}
	return mid
}

func (p *mtuProber) record(size int, ok bool) {
	p.access.Lock()
	if ok {
		if size > p.lower {
			p.lower = size
		}
	} else if size < p.upper {
		p.upper = size
	}
	p.access.Unlock()
	// Growth is applied immediately: it is verified by definition. Shrinking
	// waits for the round to settle so one lost probe can not stall transfers.
	if ok && size > p.currentSize() {
		p.current.Store(int64(size))
		p.notify(size)
	}
}

func (p *mtuProber) probe(ctx context.Context, size int) probeOutcome {
	probeID := p.probeSeq.Add(1)
	staleAcks := 0
	for attempt := 0; attempt < p.config.probeAttempts; attempt++ {
		err := p.sendProbe(probeID, size)
		if err != nil {
			// With the don't-fragment bit set the kernel refuses oversized
			// datagrams outright, which is a definitive answer.
			if errors.Is(err, syscall.EMSGSIZE) {
				return probeTooBig
			}
			// Anything else (a transient socket error, an icmp report) says
			// nothing about the path mtu.
			p.logger.Debug("uss: mtu probe of ", size, " failed: ", err)
			return probeUnknown
		}
		timeout := time.NewTimer(p.config.probeTimeout)
	waitAck:
		for {
			select {
			case ack := <-p.acks:
				if ack == probeID {
					timeout.Stop()
					return probeFits
				}
				// Acknowledgement of an earlier, already abandoned probe. It
				// must not consume an attempt: an ack that arrives late would
				// otherwise make a perfectly good size look like a black hole.
				staleAcks++
				if staleAcks >= maxStaleProbeAcks {
					timeout.Stop()
					return probeUnknown
				}
			case <-timeout.C:
				break waitAck
			case <-ctx.Done():
				timeout.Stop()
				return probeUnknown
			}
		}
	}
	// No answer at all is what a black hole looks like.
	return probeTooBig
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
