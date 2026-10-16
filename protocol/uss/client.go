package uss

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/sagernet/smux"
	kcp "github.com/xtaci/kcp-go/v5"
)

type clientConfig struct {
	server           M.Socksaddr
	psk              []byte
	tcpEnabled       bool
	kcp              kcpConfig
	mtu              mtuConfig
	pingInterval     time.Duration
	udpTimeout       time.Duration
	maxUDPFlows      int
	replayWindowSize int
}

func (c clientConfig) validate() error {
	if !c.server.IsValid() {
		return E.New("missing server address")
	}
	if len(c.psk) != keySize {
		return E.New("invalid pre shared key")
	}
	if err := c.kcp.validate(); err != nil {
		return err
	}
	if err := c.mtu.validate(); err != nil {
		return err
	}
	if c.pingInterval <= 0 {
		return E.New("ping_interval must be positive")
	}
	if c.udpTimeout <= 0 {
		return E.New("udp_timeout must be positive")
	}
	if c.maxUDPFlows < 1 {
		return E.New("max_udp_flows must be positive")
	}
	if c.replayWindowSize < 64 {
		return E.New("replay_window_size must be at least 64")
	}
	return nil
}

// clientTransport owns the single UDP socket that carries every packet of one
// outbound: probes, reliable streams and raw datagrams share it, and the packet
// type inside the encrypted envelope decides where each one goes.
type clientTransport struct {
	ctx      context.Context
	cancel   context.CancelFunc
	logger   logger.ContextLogger
	config   clientConfig
	dialer   func(ctx context.Context) (net.Conn, error)
	instance [instanceIDSize]byte
	keys     *sessionKeys

	access     sync.Mutex
	conn       net.Conn
	serverAddr net.Addr

	sendCounter atomic.Uint64
	recvWindow  *replayWindow
	prober      *mtuProber
	reassembler *reassembler

	lastPong atomic.Int64

	sessionAccess sync.Mutex
	kcpSession    *kcpSession

	udpAccess     sync.Mutex
	flowIndex     map[uint32]*clientUDPSession
	nextSessionID atomic.Uint32

	closeOnce sync.Once
	closed    atomic.Bool
	done      chan struct{}
}

func newClientTransport(parent context.Context, logger logger.ContextLogger, config clientConfig, dialer func(ctx context.Context) (net.Conn, error)) (*clientTransport, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	transport := &clientTransport{
		ctx:         ctx,
		cancel:      cancel,
		logger:      logger,
		config:      config,
		dialer:      dialer,
		recvWindow:  newReplayWindow(config.replayWindowSize),
		reassembler: newReassembler(256, 4*1024*1024, reassemblyTimeout),
		flowIndex:   make(map[uint32]*clientUDPSession),
		done:        make(chan struct{}),
	}
	_, err := io.ReadFull(rand.Reader, transport.instance[:])
	if err != nil {
		cancel()
		return nil, E.Cause(err, "generate instance id")
	}
	transport.keys, err = deriveSessionKeys(config.psk, transport.instance, true)
	if err != nil {
		cancel()
		return nil, err
	}
	conn, err := dialer(ctx)
	if err != nil {
		cancel()
		return nil, E.Cause(err, "dial server")
	}
	transport.conn = conn
	transport.serverAddr = conn.RemoteAddr()
	var seed [4]byte
	_, err = io.ReadFull(rand.Reader, seed[:])
	if err != nil {
		conn.Close()
		cancel()
		return nil, E.Cause(err, "seed session id")
	}
	transport.nextSessionID.Store(uint32(seed[0])<<24 | uint32(seed[1])<<16 | uint32(seed[2])<<8 | uint32(seed[3]))
	transport.prober = newMTUProber(config.mtu, logger, transport.sendProbe, transport.onMTUIncrease)
	go transport.readLoop(conn)
	go transport.prober.run(ctx)
	go transport.keepAliveLoop()
	return transport, nil
}

func (t *clientTransport) instanceID() [instanceIDSize]byte {
	return t.instance
}

// pathMTUSize is the largest datagram the path is currently believed to carry.
func (t *clientTransport) pathMTUSize() int {
	return t.prober.currentSize()
}

func (t *clientTransport) onMTUIncrease(size int) {
	t.sessionAccess.Lock()
	kcpSession := t.kcpSession
	t.sessionAccess.Unlock()
	if kcpSession != nil && kcpSession.arq != nil {
		kcpSession.arq.SetMtu(t.kcpMTU(size))
	}
}

// kcpMTU converts a datagram budget into the segment budget the ARQ layer may
// use, leaving room for our own envelope.
func (t *clientTransport) kcpMTU(datagramSize int) int {
	mtu := datagramSize - cleartextSize - aeadTagSize - 1
	if mtu < minKCPMTU {
		// A segment budget below this can not carry a KCP header and payload;
		// keep the ARQ layer usable instead of handing it a nonsense value.
		mtu = minKCPMTU
	}
	return mtu
}

func (t *clientTransport) nextCounter() uint64 {
	return t.sendCounter.Add(1) - 1
}

// serverAddress returns the peer address of the current socket.
func (t *clientTransport) serverAddress() net.Addr {
	t.access.Lock()
	defer t.access.Unlock()
	return t.serverAddr
}

// ensureConn returns the current socket, dialling a new one when a fatal read
// error dropped the previous one. A server restart or a route change must not
// take the whole outbound down until the process is reloaded.
func (t *clientTransport) ensureConn() (net.Conn, error) {
	t.access.Lock()
	defer t.access.Unlock()
	if t.conn != nil {
		return t.conn, nil
	}
	if t.closed.Load() {
		return nil, os.ErrClosed
	}
	conn, err := t.dialer(t.ctx)
	if err != nil {
		return nil, E.Cause(err, "redial server")
	}
	t.conn = conn
	t.serverAddr = conn.RemoteAddr()
	go t.readLoop(conn)
	return conn, nil
}

// currentConn returns the socket without dialling a new one.
func (t *clientTransport) currentConn() net.Conn {
	t.access.Lock()
	defer t.access.Unlock()
	return t.conn
}

// dropConn forgets the socket after a fatal read error so the next send dials a
// fresh one.
func (t *clientTransport) dropConn(conn net.Conn) {
	t.access.Lock()
	if t.conn == conn {
		t.conn = nil
	}
	t.access.Unlock()
	conn.Close()
}

func (t *clientTransport) sendPlaintext(plaintext []byte) error {
	conn, err := t.ensureConn()
	if err != nil {
		return err
	}
	datagram := make([]byte, 0, cleartextSize+len(plaintext)+aeadTagSize)
	datagram = t.keys.seal(datagram, t.instance, t.nextCounter(), plaintext)
	_, err = conn.Write(datagram)
	return err
}

func (t *clientTransport) sendProbe(probeID uint32, size int) error {
	bodySize := size - cleartextSize - aeadTagSize
	if bodySize < 1+probeHeaderSize {
		return E.New("probe size too small: ", size)
	}
	plaintext := make([]byte, bodySize)
	plaintext[0] = packetTypeProbe
	putUint32(plaintext[1:], probeID)
	// The padding is what makes this datagram as large as the size under test;
	// it carries no information and the peer ignores it.
	return t.sendPlaintext(plaintext)
}

func (t *clientTransport) readLoop(conn net.Conn) {
	buffer := make([]byte, maxDatagramSize)
	consecutiveErrors := 0
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			if t.closed.Load() {
				return
			}
			if isTransientNetworkError(err) {
				// A connected UDP socket surfaces ICMP errors from the path
				// here; they say nothing about the socket itself. A burst of
				// them would otherwise turn this loop into a busy spin.
				consecutiveErrors++
				if consecutiveErrors > 8 {
					t.logger.Debug("uss: transient read errors keep coming, backing off: ", err)
					if !sleepContext(t.ctx, 50*time.Millisecond) {
						return
					}
				} else {
					t.logger.Debug("uss: transient read error: ", err)
				}
				continue
			}
			// The socket itself is done. Everything above it (KCP sessions,
			// smux streams, flows) survives a socket swap, so only the socket
			// is dropped and the next send dials a new one.
			t.logger.Error("uss: read failed, dropping socket: ", err)
			t.dropConn(conn)
			return
		}
		consecutiveErrors = 0
		t.handleDatagram(buffer[:n])
	}
}

func (t *clientTransport) handleDatagram(datagram []byte) {
	if len(datagram) < cleartextSize+aeadTagSize {
		return
	}
	if getInstanceID(datagram) != t.instance {
		return
	}
	counter := getCounter(datagram[instanceIDSize:])
	plaintext, err := t.keys.open(datagram)
	if err != nil {
		// Also the path for unsolicited packets: nothing to log.
		return
	}
	// Replay checking happens after authentication: the counter is only
	// trustworthy once the tag verified, otherwise a forged counter could
	// invalidate every legitimate packet that follows.
	if !t.recvWindow.accept(counter) {
		return
	}
	if len(plaintext) == 0 {
		return
	}
	t.prober.observeReceived(len(datagram))
	switch plaintext[0] {
	case packetTypeProbeAck:
		if len(plaintext) < 1+probeHeaderSize {
			return
		}
		t.prober.handleAck(readUint32(plaintext[1:]))
	case packetTypePong:
		t.lastPong.Store(time.Now().UnixNano())
	case packetTypePing:
		_ = t.sendPlaintext([]byte{packetTypePong})
	case packetTypeProbe:
		// Server initiated probing: answer with the same probe id, the padding
		// is ignored on both sides.
		if len(plaintext) < 1+probeHeaderSize {
			return
		}
		ack := make([]byte, 1+probeHeaderSize)
		ack[0] = packetTypeProbeAck
		copy(ack[1:], plaintext[1:1+probeHeaderSize])
		_ = t.sendPlaintext(ack)
	case packetTypeKCP:
		t.sessionAccess.Lock()
		kcpSession := t.kcpSession
		t.sessionAccess.Unlock()
		if kcpSession != nil && kcpSession.virtual != nil {
			kcpSession.virtual.push(plaintext[1:])
		}
	case packetTypeUDPReply:
		t.handleRawReply(plaintext[1:])
	default:
		t.logger.Debug("uss: unknown packet type ", plaintext[0])
	}
}

func (t *clientTransport) handleRawReply(body []byte) {
	if len(body) < rawHeaderSize {
		return
	}
	sessionID := readUint32(body)
	fragmentID := readUint32(body[4:])
	fragmentIndex := body[8]
	fragmentTotal := body[9]
	payload := body[rawHeaderSize:]
	if fragmentTotal > 1 {
		assembled, complete := t.reassembler.add(fragmentKey{sessionID: sessionID, fragmentID: fragmentID}, fragmentIndex, fragmentTotal, payload)
		if !complete {
			return
		}
		payload = assembled
	} else {
		// The read buffer is reused for the next datagram, so anything queued
		// for a session has to be copied out of it first.
		payload = copyPayload(payload)
	}
	t.udpAccess.Lock()
	session := t.flowIndex[sessionID]
	t.udpAccess.Unlock()
	if session == nil {
		return
	}
	session.deliver(sessionID, payload)
}

// kcp returns the shared reliable channel, creating it on first use. One
// session per outbound keeps short-lived TCP connections cheap: they become
// streams instead of handshakes.
func (t *clientTransport) kcp() (*kcpSession, error) {
	if !t.config.tcpEnabled {
		return nil, E.New("uss: tcp is disabled for this outbound")
	}
	t.sessionAccess.Lock()
	defer t.sessionAccess.Unlock()
	if t.kcpSession != nil && !t.kcpSession.closed.Load() {
		return t.kcpSession, nil
	}
	if t.closed.Load() {
		return nil, os.ErrClosed
	}
	conv, err := newConv()
	if err != nil {
		return nil, err
	}
	session := &kcpSession{conv: conv}
	conn, err := t.ensureConn()
	if err != nil {
		return nil, err
	}
	session.virtual = newKCPPacketConn(func(payload []byte) error {
		plaintext := make([]byte, 1+len(payload))
		plaintext[0] = packetTypeKCP
		copy(plaintext[1:], payload)
		return t.sendPlaintext(plaintext)
	}, t.serverAddress(), conn.LocalAddr())
	arq, err := kcp.NewConn3(conv, t.serverAddr, nil, 0, 0, session.virtual)
	if err != nil {
		session.virtual.Close()
		return nil, E.Cause(err, "create kcp session")
	}
	session.arq = arq
	t.config.kcp.apply(arq)
	arq.SetMtu(t.kcpMTU(t.pathMTUSize()))
	muxSession, err := smux.Client(arq, t.config.kcp.smuxConfig())
	if err != nil {
		arq.Close()
		session.virtual.Close()
		return nil, E.Cause(err, "create smux session")
	}
	session.mux = muxSession
	t.kcpSession = session
	t.logger.Debug("uss: reliable channel ", conv, " established")
	return session, nil
}

// resetKCP drops the shared reliable channel so the next dial builds a fresh
// one. It is used when the channel is detected dead (interface change, server
// restart) instead of failing the connection that noticed it.
func (t *clientTransport) resetKCP(session *kcpSession) {
	t.sessionAccess.Lock()
	if t.kcpSession == session {
		t.kcpSession = nil
	}
	t.sessionAccess.Unlock()
	session.close()
}

// resetKCPIfPresent drops the shared reliable channel, if one exists.
func (t *clientTransport) resetKCPIfPresent() {
	t.sessionAccess.Lock()
	session := t.kcpSession
	t.kcpSession = nil
	t.sessionAccess.Unlock()
	if session != nil {
		session.close()
	}
}

func (t *clientTransport) registerFlow(flowID uint32, session *clientUDPSession) {
	t.udpAccess.Lock()
	t.flowIndex[flowID] = session
	t.udpAccess.Unlock()
}

func (t *clientTransport) unregisterFlow(flowID uint32) {
	t.udpAccess.Lock()
	delete(t.flowIndex, flowID)
	t.udpAccess.Unlock()
}

func (t *clientTransport) nextFlowID() uint32 {
	return t.nextSessionID.Add(1)
}

func (t *clientTransport) keepAliveLoop() {
	interval := t.config.pingInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	started := time.Now()
	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			err := t.sendPlaintext([]byte{packetTypePing})
			if err != nil && !t.closed.Load() {
				t.logger.Debug("uss: keep alive failed: ", err)
			}
			// Half received datagrams are only expired when a fragment arrives,
			// so sweep them on the ping tick as well.
			t.reassembler.expire()
			// A server that stops answering is not detectable from the send
			// side, so the reliable channel is dropped once the silence is
			// clearly longer than the ping interval; the next dial builds a
			// new one instead of hanging on a dead path.
			lastSeen := t.lastPong.Load()
			if lastSeen == 0 {
				lastSeen = started.UnixNano()
			}
			if time.Since(time.Unix(0, lastSeen)) > 3*interval {
				t.logger.Warn("uss: no response from ", t.serverAddress(), " for ", 3*interval, ", dropping the reliable channel")
				t.lastPong.Store(time.Now().UnixNano())
				t.resetKCPIfPresent()
			}
		}
	}
}

func (t *clientTransport) close() {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		close(t.done)
		t.cancel()
		t.sessionAccess.Lock()
		kcpSession := t.kcpSession
		t.kcpSession = nil
		t.sessionAccess.Unlock()
		if kcpSession != nil {
			kcpSession.close()
		}
		t.udpAccess.Lock()
		sessions := t.flowIndex
		t.flowIndex = make(map[uint32]*clientUDPSession)
		t.udpAccess.Unlock()
		for _, session := range sessions {
			session.Close()
		}
		t.reassembler.close()
		t.access.Lock()
		conn := t.conn
		t.conn = nil
		t.access.Unlock()
		if conn != nil {
			conn.Close()
		}
	})
}

// isTransientNetworkError reports whether an error from a UDP socket describes
// the path rather than the socket.
func isTransientNetworkError(err error) bool {
	for _, target := range []error{
		syscall.ECONNREFUSED,
		syscall.EHOSTUNREACH,
		syscall.ENETUNREACH,
		syscall.EMSGSIZE,
		syscall.EHOSTDOWN,
		syscall.ENETDOWN,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return true
	}
	return false
}
