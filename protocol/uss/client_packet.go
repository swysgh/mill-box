package uss

import (
	"context"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// readQueueSize bounds how many datagrams may wait to be handed to the router.
// UDP is lossy by contract, so a full queue drops instead of blocking the read
// loop, which would stall every other session on the same socket.
const readQueueSize = 256

type clientFlow struct {
	id          uint32
	lastActive  atomic.Int64
	fragmentSeq atomic.Uint32
}

func (f *clientFlow) nextFragmentID() uint32 {
	return f.fragmentSeq.Add(1)
}

type udpReply struct {
	destination M.Socksaddr
	payload     []byte
}

// clientUDPSession is the packet connection the router gets for a UDP flow. A
// single session carries datagrams for as many destinations as the caller
// needs, which is what a tun inbound produces: each destination gets its own
// flow id so replies can be mapped back without keeping a socket per target.
type clientUDPSession struct {
	transport *clientTransport
	logger    logger.ContextLogger
	ctx       context.Context
	cancel    context.CancelFunc

	access       sync.Mutex
	flows        map[M.Socksaddr]*clientFlow
	destinations map[uint32]M.Socksaddr
	remoteAddr   M.Socksaddr

	readCh chan udpReply

	deadlineAccess sync.Mutex
	readDeadline   time.Time
	// A read that is already waiting has to notice a new deadline, otherwise
	// SetReadDeadline could never interrupt it.
	deadlineChanged chan struct{}

	closeOnce sync.Once
	done      chan struct{}
}

var _ N.NetPacketConn = (*clientUDPSession)(nil)

func newClientUDPSession(ctx context.Context, transport *clientTransport, logger logger.ContextLogger) *clientUDPSession {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &clientUDPSession{
		transport:       transport,
		logger:          logger,
		ctx:             sessionCtx,
		cancel:          cancel,
		flows:           make(map[M.Socksaddr]*clientFlow),
		destinations:    make(map[uint32]M.Socksaddr),
		readCh:          make(chan udpReply, readQueueSize),
		done:            make(chan struct{}),
		deadlineChanged: make(chan struct{}, 1),
	}
	go session.expireLoop()
	return session
}

func (s *clientUDPSession) flowFor(destination M.Socksaddr) *clientFlow {
	s.access.Lock()
	defer s.access.Unlock()
	flow, loaded := s.flows[destination]
	if loaded {
		return flow
	}
	if len(s.flows) >= s.transport.config.maxUDPFlows {
		s.evictOldestLocked()
	}
	flow = &clientFlow{id: s.transport.nextFlowID()}
	flow.lastActive.Store(time.Now().UnixNano())
	s.flows[destination] = flow
	s.destinations[flow.id] = destination
	s.transport.registerFlow(flow.id, s)
	return flow
}

// evictOldestLocked drops the least recently used flow. Only the mapping is
// released: the server side expires its own flow state, and a later datagram to
// the same destination simply allocates a new id.
func (s *clientUDPSession) evictOldestLocked() {
	var (
		oldestKey   M.Socksaddr
		oldestFlow  *clientFlow
		oldestStamp int64
	)
	for key, flow := range s.flows {
		stamp := flow.lastActive.Load()
		if oldestFlow == nil || stamp < oldestStamp {
			oldestKey, oldestFlow, oldestStamp = key, flow, stamp
		}
	}
	if oldestFlow == nil {
		return
	}
	s.releaseFlowLocked(oldestKey, oldestFlow)
}

func (s *clientUDPSession) releaseFlowLocked(key M.Socksaddr, flow *clientFlow) {
	delete(s.flows, key)
	delete(s.destinations, flow.id)
	s.transport.unregisterFlow(flow.id)
}

func (s *clientUDPSession) expireLoop() {
	interval := s.transport.config.udpTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			deadline := time.Now().Add(-s.transport.config.udpTimeout).UnixNano()
			s.access.Lock()
			for key, flow := range s.flows {
				if flow.lastActive.Load() < deadline {
					s.releaseFlowLocked(key, flow)
				}
			}
			s.access.Unlock()
		}
	}
}

// deliver hands a decrypted reply to the waiting reader.
func (s *clientUDPSession) deliver(flowID uint32, payload []byte) {
	s.access.Lock()
	destination, loaded := s.destinations[flowID]
	flow := s.flows[destination]
	s.access.Unlock()
	if !loaded || flow == nil {
		return
	}
	flow.lastActive.Store(time.Now().UnixNano())
	s.access.Lock()
	s.remoteAddr = destination
	s.access.Unlock()
	select {
	case s.readCh <- udpReply{destination: destination, payload: payload}:
	default:
		s.logger.Debug("uss: dropping reply to ", destination, ": queue full")
	}
}

func (s *clientUDPSession) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	// The caller transfers ownership of the buffer.
	defer buffer.Release()
	if !destination.IsValid() {
		return os.ErrInvalid
	}
	if s.transport.closed.Load() {
		return os.ErrClosed
	}
	flow := s.flowFor(destination)
	flow.lastActive.Store(time.Now().UnixNano())
	addressLength := M.SocksaddrSerializer.AddrPortLen(destination)
	overhead := 1 + rawHeaderSize + addressLength
	// Every fragment must fit in one datagram, so the usable chunk size
	// follows the path mtu that the probe settled on.
	chunkLimit := s.transport.pathMTUSize() - cleartextSize - aeadTagSize - overhead
	if chunkLimit < 1 {
		chunkLimit = 1
	}
	payload := buffer.Bytes()
	chunks := fragmentPayload(payload, chunkLimit)
	fragmentID := flow.nextFragmentID()
	for index, chunk := range chunks {
		plaintext := make([]byte, overhead+len(chunk))
		plaintext[0] = packetTypeUDP
		writeRawHeader(plaintext[1:], flow.id, fragmentID, uint8(index), uint8(len(chunks)))
		writer := buf.With(plaintext[1+rawHeaderSize:])
		err := M.SocksaddrSerializer.WriteAddrPort(writer, destination)
		if err != nil {
			return err
		}
		addressWritten := writer.Len()
		copy(plaintext[1+rawHeaderSize+addressWritten:], chunk)
		err = s.transport.sendPlaintext(plaintext)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *clientUDPSession) WriteTo(p []byte, addr net.Addr) (int, error) {
	// WritePacket takes ownership of the buffer, so the caller's slice must not
	// be handed over directly.
	copied := buf.NewSize(len(p))
	_, _ = copied.Write(p)
	err := s.WritePacket(copied, M.SocksaddrFromNet(addr))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *clientUDPSession) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	reply, err := s.readReply()
	if err != nil {
		return M.Socksaddr{}, err
	}
	_, err = buffer.Write(reply.payload)
	if err != nil {
		return M.Socksaddr{}, err
	}
	return reply.destination, nil
}

func (s *clientUDPSession) ReadFrom(p []byte) (int, net.Addr, error) {
	reply, err := s.readReply()
	if err != nil {
		return 0, nil, err
	}
	n := copy(p, reply.payload)
	return n, reply.destination.UDPAddr(), nil
}

func (s *clientUDPSession) readReply() (udpReply, error) {
	for {
		s.deadlineAccess.Lock()
		deadline := s.readDeadline
		s.deadlineAccess.Unlock()
		var timeout <-chan time.Time
		var timer *time.Timer
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case reply := <-s.readCh:
			if timer != nil {
				timer.Stop()
			}
			return reply, nil
		case <-s.done:
			if timer != nil {
				timer.Stop()
			}
			return udpReply{}, os.ErrClosed
		case <-timeout:
			return udpReply{}, os.ErrDeadlineExceeded
		case <-s.deadlineChanged:
			if timer != nil {
				timer.Stop()
			}
			continue
		}
	}
}

func (s *clientUDPSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.cancel()
		s.access.Lock()
		for key, flow := range s.flows {
			s.releaseFlowLocked(key, flow)
		}
		s.access.Unlock()
	})
	return nil
}

func (s *clientUDPSession) LocalAddr() net.Addr {
	conn := s.transport.currentConn()
	if conn == nil {
		return &net.UDPAddr{}
	}
	return conn.LocalAddr()
}

func (s *clientUDPSession) RemoteAddr() net.Addr {
	s.access.Lock()
	defer s.access.Unlock()
	return s.remoteAddr.UDPAddr()
}

func (s *clientUDPSession) SetDeadline(t time.Time) error {
	s.deadlineAccess.Lock()
	s.readDeadline = t
	s.deadlineAccess.Unlock()
	select {
	case s.deadlineChanged <- struct{}{}:
	default:
	}
	return nil
}

func (s *clientUDPSession) SetReadDeadline(t time.Time) error {
	return s.SetDeadline(t)
}

func (s *clientUDPSession) SetWriteDeadline(t time.Time) error {
	return nil
}
