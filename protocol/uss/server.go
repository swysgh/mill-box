package uss

import (
	"bytes"
	"context"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/swysgh/mill-box/adapter"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/log"

	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/sagernet/smux"
)

type serverConfig struct {
	psk              []byte
	tcpEnabled       bool
	udpEnabled       bool
	kcp              kcpConfig
	mtu              mtuConfig
	sessionTimeout   time.Duration
	udpTimeout       time.Duration
	maxSessions      int
	maxUDPFlows      int
	maxKCPChannels   int
	replayWindowSize int
	handshakeTimeout time.Duration
}

func (c serverConfig) validate() error {
	if len(c.psk) != keySize {
		return E.New("invalid pre shared key")
	}
	if !c.tcpEnabled && !c.udpEnabled {
		return E.New("both tcp and udp are disabled")
	}
	if err := c.kcp.validate(); err != nil {
		return err
	}
	if err := c.mtu.validate(); err != nil {
		return err
	}
	if c.sessionTimeout <= 0 {
		return E.New("session_timeout must be positive")
	}
	if c.udpTimeout <= 0 {
		return E.New("udp_timeout must be positive")
	}
	if c.maxSessions < 1 {
		return E.New("max_sessions must be positive")
	}
	if c.maxUDPFlows < 1 {
		return E.New("max_udp_flows must be positive")
	}
	if c.maxKCPChannels < 1 {
		return E.New("max_kcp_channels must be positive")
	}
	if c.replayWindowSize < 64 {
		return E.New("replay_window_size must be at least 64")
	}
	if c.handshakeTimeout <= 0 {
		return E.New("handshake_timeout must be positive")
	}
	return nil
}

// serverTransport owns the inbound side of the protocol: it authenticates every
// datagram, keeps one session per client instance and dispatches the three
// kinds of payload (probes, reliable streams, raw datagrams) to their handlers.
type serverTransport struct {
	ctx        context.Context
	cancel     context.CancelFunc
	logger     logger.ContextLogger
	config     serverConfig
	router     adapter.ConnectionRouterEx
	inboundTag string
	writer     N.PacketWriter
	localAddr  net.Addr

	keys *keyCache

	access   sync.Mutex
	sessions map[[instanceIDSize]byte]*serverSession

	closeOnce sync.Once
	closed    atomic.Bool
	done      chan struct{}
}

func newServerTransport(parent context.Context, logger logger.ContextLogger, config serverConfig, router adapter.ConnectionRouterEx, inboundTag string, writer N.PacketWriter, localAddr net.Addr) (*serverTransport, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	transport := &serverTransport{
		ctx:        ctx,
		cancel:     cancel,
		logger:     logger,
		config:     config,
		router:     router,
		inboundTag: inboundTag,
		writer:     writer,
		localAddr:  localAddr,
		keys:       newKeyCache(config.maxSessions * 2),
		sessions:   make(map[[instanceIDSize]byte]*serverSession),
		done:       make(chan struct{}),
	}
	go transport.janitor()
	return transport, nil
}

// handlePacket is called for every datagram that reached the listener.
func (t *serverTransport) handlePacket(datagram []byte, source M.Socksaddr) {
	if t.closed.Load() {
		return
	}
	if len(datagram) < cleartextSize+aeadTagSize {
		return
	}
	instanceID := getInstanceID(datagram)
	keys, loaded := t.keys.get(t.config.psk, instanceID)
	if !loaded {
		return
	}
	plaintext, err := keys.open(datagram)
	if err != nil {
		// Wrong key, forged instance id or plain garbage. Never allocate, never
		// log per packet: this is also the port scan path.
		return
	}
	if len(plaintext) == 0 {
		return
	}
	session, err := t.sessionFor(instanceID, source, keys)
	if err != nil {
		return
	}
	// Replay checking must happen after authentication: an unauthenticated
	// counter would let a third party push the window forward and lock out the
	// real client.
	if !session.recvWindow.accept(getCounter(datagram[instanceIDSize:])) {
		return
	}
	session.touch(source)
	// A datagram that arrived proves that its size fits the path, which is how
	// the server follows the client's discovery without probing itself.
	session.prober.observeReceived(len(datagram))
	switch plaintext[0] {
	case packetTypeProbe:
		if len(plaintext) < 1+probeHeaderSize {
			return
		}
		_ = t.sendToClient(session, packetTypeProbeAck, plaintext[1:1+probeHeaderSize])
	case packetTypePing:
		_ = t.sendToClient(session, packetTypePong, nil)
	case packetTypeKCP:
		if !t.config.tcpEnabled {
			return
		}
		session.handleKCP(t, plaintext[1:])
	case packetTypeUDP:
		if !t.config.udpEnabled {
			return
		}
		session.handleRaw(t, plaintext[1:])
	default:
	}
}

func (t *serverTransport) sessionFor(instanceID [instanceIDSize]byte, source M.Socksaddr, keys *sessionKeys) (*serverSession, error) {
	t.access.Lock()
	session, loaded := t.sessions[instanceID]
	if loaded {
		t.access.Unlock()
		return session, nil
	}
	if len(t.sessions) >= t.config.maxSessions {
		t.evictLocked()
	}
	session = newServerSession(t, instanceID, keys, source)
	t.sessions[instanceID] = session
	// The count has to be taken while the lock is held: reading the map after
	// unlocking races with every other session creation.
	active := len(t.sessions)
	t.access.Unlock()
	t.logger.Debug("uss: new session from ", source, " (", active, " active)")
	return session, nil
}

// evictLocked drops the session that has been idle the longest. The caller must
// hold the lock.
func (t *serverTransport) evictLocked() {
	var (
		oldestID   [instanceIDSize]byte
		oldest     *serverSession
		oldestSeen int64
	)
	for instanceID, session := range t.sessions {
		seen := session.lastActive.Load()
		if oldest == nil || seen < oldestSeen {
			oldestID, oldest, oldestSeen = instanceID, session, seen
		}
	}
	if oldest == nil {
		return
	}
	delete(t.sessions, oldestID)
	t.logger.Debug("uss: evicting session from ", oldest.clientAddr.Load(), " to make room")
	oldest.close()
	// Without this the key of an evicted client stays resident until the cache
	// overflows, and a returning client would keep a session-less key.
	t.keys.remove(oldestID)
}

func (t *serverTransport) janitor() {
	interval := t.config.sessionTimeout / 4
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			deadline := time.Now().Add(-t.config.sessionTimeout).UnixNano()
			t.access.Lock()
			var expired []*serverSession
			for instanceID, session := range t.sessions {
				if session.lastActive.Load() < deadline {
					delete(t.sessions, instanceID)
					expired = append(expired, session)
				}
			}
			t.access.Unlock()
			for _, session := range expired {
				t.logger.Debug("uss: session from ", session.clientAddr.Load(), " expired")
				session.close()
				t.keys.remove(session.instanceID)
			}
			// Half received datagrams are only expired when another fragment
			// arrives, so a peer that stops mid datagram would otherwise hold
			// its buffers until the session ends.
			t.access.Lock()
			live := make([]*serverSession, 0, len(t.sessions))
			for _, session := range t.sessions {
				live = append(live, session)
			}
			t.access.Unlock()
			for _, session := range live {
				session.reassembler.expire()
			}
		}
	}
}

// sendToClient wraps one payload in the encrypted envelope and hands it to the
// listener's packet writer, which owns the buffer from there on.
func (t *serverTransport) sendToClient(session *serverSession, packetType byte, body []byte) error {
	if t.closed.Load() {
		return os.ErrClosed
	}
	plaintext := make([]byte, 1+len(body))
	plaintext[0] = packetType
	copy(plaintext[1:], body)
	datagram := make([]byte, 0, cleartextSize+len(plaintext)+aeadTagSize)
	datagram = session.keys.seal(datagram, session.instanceID, session.nextSendCounter(), plaintext)
	if len(datagram) > maxDatagramSize {
		// Instrumentation: see the client side. A datagram this large can not
		// be received by a peer that sizes its buffer to maxDatagramSize.
		t.logger.Warn("uss: sending oversized datagram: ", len(datagram), " bytes (limit ", maxDatagramSize, ")")
	}
	destination := session.clientAddr.Load()
	if !destination.IsValid() {
		return os.ErrClosed
	}
	return t.writer.WritePacket(buf.As(datagram), destination)
}

func (t *serverTransport) close() {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		close(t.done)
		t.cancel()
		t.access.Lock()
		sessions := t.sessions
		t.sessions = make(map[[instanceIDSize]byte]*serverSession)
		t.access.Unlock()
		for _, session := range sessions {
			session.close()
		}
	})
}

// serverSession holds everything scoped to one client instance.
type serverSession struct {
	transport  *serverTransport
	instanceID [instanceIDSize]byte
	keys       *sessionKeys
	prober     *mtuProber
	recvWindow *replayWindow

	clientAddr socksaddrBox

	access      sync.Mutex
	udpFlows    map[flowKey]*serverFlowConn
	kcpChannels map[uint32]*serverKCPChannel
	reassembler *reassembler

	ctx    context.Context
	cancel context.CancelFunc

	sendCounter atomic.Uint64
	lastActive  atomic.Int64
	closeOnce   sync.Once
	closed      atomic.Bool
}

type flowKey struct {
	sessionID   uint32
	destination M.Socksaddr
}

func newServerSession(transport *serverTransport, instanceID [instanceIDSize]byte, keys *sessionKeys, source M.Socksaddr) *serverSession {
	session := &serverSession{
		transport:   transport,
		instanceID:  instanceID,
		keys:        keys,
		recvWindow:  newReplayWindow(transport.config.replayWindowSize),
		udpFlows:    make(map[flowKey]*serverFlowConn),
		kcpChannels: make(map[uint32]*serverKCPChannel),
		reassembler: newReassembler(512, 8*1024*1024, reassemblyTimeout),
	}
	session.ctx, session.cancel = context.WithCancel(transport.ctx)
	session.clientAddr.Store(source)
	session.lastActive.Store(time.Now().UnixNano())
	// The server never probes unless it is explicitly asked to: the client
	// discovers the path, and every datagram that arrives is evidence of a size
	// that fits, so passive observation is enough.
	session.prober = newMTUProber(transport.config.mtu, transport.logger, func(probeID uint32, size int) error {
		return transport.sendProbeToClient(session, probeID, size)
	}, session.applyPathMTU)
	if transport.config.mtu.enabled {
		go session.prober.run(session.ctx)
	}
	return session
}

// sendProbeToClient emits a probe padded to the size under test, mirroring the
// client side so that server-initiated probing works the same way.
func (t *serverTransport) sendProbeToClient(session *serverSession, probeID uint32, size int) error {
	bodySize := size - cleartextSize - aeadTagSize - 1
	if bodySize < probeHeaderSize {
		return E.New("probe size too small: ", size)
	}
	body := make([]byte, bodySize)
	putUint32(body, probeID)
	return t.sendToClient(session, packetTypeProbe, body)
}

// applyPathMTU keeps the reliable channels in step with what the session has
// learned about the path: a channel created before the estimate grew would
// otherwise stay stuck at the initial, conservative segment size.
func (s *serverSession) applyPathMTU(size int) {
	mtu := s.kcpMTU()
	s.access.Lock()
	channels := make([]*serverKCPChannel, 0, len(s.kcpChannels))
	for _, channel := range s.kcpChannels {
		channels = append(channels, channel)
	}
	s.access.Unlock()
	for _, channel := range channels {
		channel.arq.SetMtu(mtu)
	}
}

func (s *serverSession) nextSendCounter() uint64 {
	return s.sendCounter.Add(1) - 1
}

func (s *serverSession) touch(source M.Socksaddr) {
	s.lastActive.Store(time.Now().UnixNano())
	if source.IsValid() {
		s.clientAddr.Store(source)
	}
}

// pathMTUSize is this session's estimate of the largest datagram the path to
// the client carries.
func (s *serverSession) pathMTUSize() int {
	return s.prober.currentSize()
}

func (s *serverSession) handleRaw(transport *serverTransport, body []byte) {
	if len(body) < rawHeaderSize {
		return
	}
	sessionID := readUint32(body)
	fragmentID := readUint32(body[4:])
	fragmentIndex := body[8]
	fragmentTotal := body[9]
	rest := body[rawHeaderSize:]
	reader := bytes.NewReader(rest)
	destination, err := M.SocksaddrSerializer.ReadAddrPort(reader)
	if err != nil {
		return
	}
	// The serializer consumed the address, so what is left is the payload.
	payload := rest[len(rest)-reader.Len():]
	if fragmentTotal > 1 {
		assembled, complete := s.reassembler.add(fragmentKey{sessionID: sessionID, fragmentID: fragmentID}, fragmentIndex, fragmentTotal, payload)
		if !complete {
			return
		}
		payload = assembled
	} else {
		// The listener hands us a buffer it reuses, so a datagram that is
		// queued for the router has to be copied out of it first.
		payload = copyPayload(payload)
	}
	key := flowKey{sessionID: sessionID, destination: destination}
	flow, loaded := s.flowFor(transport, key)
	if !loaded {
		return
	}
	flow.deliver(payload)
}

func (s *serverSession) flowFor(transport *serverTransport, key flowKey) (*serverFlowConn, bool) {
	s.access.Lock()
	if flow, loaded := s.udpFlows[key]; loaded {
		s.access.Unlock()
		return flow, true
	}
	if len(s.udpFlows) >= transport.config.maxUDPFlows {
		s.access.Unlock()
		transport.logger.Debug("uss: session from ", s.clientAddr.Load(), " reached max_udp_flows, dropping datagram to ", key.destination)
		return nil, false
	}
	flow := newServerFlowConn(transport, s, key)
	s.udpFlows[key] = flow
	s.access.Unlock()
	go flow.run()
	return flow, true
}

func (s *serverSession) removeFlow(key flowKey, flow *serverFlowConn) {
	s.access.Lock()
	current, loaded := s.udpFlows[key]
	if loaded && current == flow {
		delete(s.udpFlows, key)
	}
	s.access.Unlock()
}

func (s *serverSession) handleKCP(transport *serverTransport, segment []byte) {
	// The KCP header starts with the conversation id, and kcp-go encodes it in
	// little endian.
	if len(segment) < kcpHeaderSize {
		return
	}
	conv := uint32(segment[0]) | uint32(segment[1])<<8 | uint32(segment[2])<<16 | uint32(segment[3])<<24
	s.access.Lock()
	channel, loaded := s.kcpChannels[conv]
	if loaded {
		s.access.Unlock()
		channel.touch()
		channel.virtual.push(segment)
		return
	}
	// Every reliable channel costs a KCP state machine and a handful of
	// goroutines, so a client can not open unbounded ones. The limit is per
	// session, which is per client instance, so hitting it only ever hurts the
	// peer that caused it.
	if len(s.kcpChannels) >= transport.config.maxKCPChannels {
		s.access.Unlock()
		transport.logger.Debug("uss: session from ", s.clientAddr.Load(), " reached max_kcp_channels, dropping segment")
		return
	}
	channel, err := newServerKCPChannel(s.instanceID, conv, s.clientAddr.Load(), func(payload []byte) error {
		return transport.sendToClient(s, packetTypeKCP, payload)
	}, transport.localAddr, transport.config.kcp)
	if err != nil {
		s.access.Unlock()
		transport.logger.Error("uss: create reliable channel: ", err)
		return
	}
	channel.arq.SetMtu(s.kcpMTU())
	s.kcpChannels[conv] = channel
	s.access.Unlock()
	transport.logger.Debug("uss: reliable channel ", conv, " from ", s.clientAddr.Load())
	go channel.serveStreams(transport.logger, transport.ctx, func(stream *smux.Stream) {
		transport.handleStream(s, stream)
	})
	go s.expireKCPChannel(channel)
	channel.virtual.push(segment)
}

// kcpMTU converts this session's datagram budget into the segment budget the
// ARQ layer may use.
func (s *serverSession) kcpMTU() int {
	return kcpMTUBudget(s.pathMTUSize())
}

func (s *serverSession) expireKCPChannel(channel *serverKCPChannel) {
	interval := s.transport.config.sessionTimeout / 4
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.transport.done:
			return
		case <-ticker.C:
			if channel.idleFor() < s.transport.config.sessionTimeout {
				continue
			}
			s.access.Lock()
			if current, loaded := s.kcpChannels[channel.conv]; loaded && current == channel {
				delete(s.kcpChannels, channel.conv)
			}
			s.access.Unlock()
			s.transport.logger.Debug("uss: reliable channel ", channel.conv, " idle, closing")
			channel.close()
			return
		}
	}
}

// handleStream reads the destination header and routes the multiplexed stream
// as an ordinary connection, which is what makes tcp_mode: kcp transparent to
// the rest of the router.
func (t *serverTransport) handleStream(session *serverSession, stream *smux.Stream) {
	// The destination header is the first thing on the stream, so a client that
	// opens a stream and sends nothing would otherwise pin this goroutine and
	// the smux stream forever. The deadline is cleared right after the header,
	// because the connection itself is long lived.
	err := stream.SetReadDeadline(time.Now().Add(t.config.handshakeTimeout))
	if err != nil {
		stream.Close()
		return
	}
	destination, err := M.SocksaddrSerializer.ReadAddrPort(stream)
	if err == nil {
		err = stream.SetReadDeadline(time.Time{})
	}
	if err != nil {
		t.logger.Debug("uss: read stream destination: ", err)
		stream.Close()
		return
	}
	ctx := log.ContextWithNewID(t.ctx)
	metadata := adapter.InboundContext{
		Inbound:     t.inboundTag,
		InboundType: C.TypeUss,
		Source:      session.clientAddr.Load(),
		Destination: destination,
		Network:     N.NetworkTCP,
	}
	t.logger.InfoContext(ctx, "inbound connection to ", destination)
	err = t.router.RouteConnection(adapter.WithContext(ctx, &metadata), stream, metadata)
	if err != nil {
		t.logger.DebugContext(ctx, "process connection to ", destination, ": ", err)
	}
	stream.Close()
}

func (s *serverSession) close() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		if s.cancel != nil {
			s.cancel()
		}
		s.access.Lock()
		flows := s.udpFlows
		s.udpFlows = make(map[flowKey]*serverFlowConn)
		channels := s.kcpChannels
		s.kcpChannels = make(map[uint32]*serverKCPChannel)
		s.access.Unlock()
		for _, flow := range flows {
			flow.Close()
		}
		for _, channel := range channels {
			channel.close()
		}
		s.reassembler.close()
	})
}
