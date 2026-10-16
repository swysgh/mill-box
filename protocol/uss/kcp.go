package uss

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/sagernet/smux"
	kcp "github.com/xtaci/kcp-go/v5"
)

// kcpConfig mirrors the tunables of the ARQ layer. Every value is settable from
// the configuration file; the defaults below are kcp-go's own.
type kcpConfig struct {
	noDelay             int
	interval            int
	resend              int
	noCongestionControl int
	windowSize          int
	maxStreamBuffer     int
	maxReceiveBuffer    int
	keepAliveInterval   time.Duration
	keepAliveTimeout    time.Duration
}

func defaultKCPConfig() kcpConfig {
	return kcpConfig{
		noDelay:             1,
		interval:            10,
		resend:              2,
		noCongestionControl: 1,
		windowSize:          512,
		maxStreamBuffer:     65536,
		maxReceiveBuffer:    4194304,
		keepAliveInterval:   10 * time.Second,
		keepAliveTimeout:    30 * time.Second,
	}
}

func (c kcpConfig) validate() error {
	if c.noDelay < 0 {
		return E.New("kcp nodelay must not be negative")
	}
	if c.interval < 0 {
		return E.New("kcp interval must not be negative")
	}
	if c.resend < 0 {
		return E.New("kcp resend must not be negative")
	}
	if c.noCongestionControl < 0 {
		return E.New("kcp nc must not be negative")
	}
	if c.windowSize < 1 {
		return E.New("kcp window_size must be positive")
	}
	if c.maxStreamBuffer < 1 {
		return E.New("kcp max_stream_buffer must be positive")
	}
	if c.maxReceiveBuffer < c.maxStreamBuffer {
		return E.New("kcp max_receive_buffer must not be smaller than max_stream_buffer")
	}
	if c.maxReceiveBuffer > 1<<31-1 {
		return E.New("kcp max_receive_buffer is too large")
	}
	if c.keepAliveInterval <= 0 {
		return E.New("kcp keep_alive_interval must be positive")
	}
	if c.keepAliveTimeout < c.keepAliveInterval {
		return E.New("kcp keep_alive_timeout must not be smaller than keep_alive_interval")
	}
	return nil
}

func (c kcpConfig) smuxConfig() *smux.Config {
	return &smux.Config{
		Version:           1,
		KeepAliveInterval: c.keepAliveInterval,
		KeepAliveTimeout:  c.keepAliveTimeout,
		MaxFrameSize:      32768,
		MaxReceiveBuffer:  c.maxReceiveBuffer,
		MaxStreamBuffer:   c.maxStreamBuffer,
	}
}

func (c kcpConfig) apply(session *kcp.UDPSession) {
	session.SetNoDelay(c.noDelay, c.interval, c.resend, c.noCongestionControl)
	session.SetWindowSize(c.windowSize, c.windowSize)
	session.SetACKNoDelay(true)
	// A byte stream is what smux expects, so let the ARQ merge writes into full
	// sized segments instead of preserving datagram boundaries.
	session.SetStreamMode(true)
}

// kcpPacketConn presents one KCP session as a net.PacketConn. kcp-go talks to
// its transport exclusively through ReadFrom/WriteTo, which lets every KCP
// segment travel inside our own encrypted envelope on the shared socket instead
// of opening a second UDP port.
type kcpPacketConn struct {
	writeFunc  func(payload []byte) error
	remoteAddr net.Addr
	localAddr  net.Addr

	readCh chan []byte

	access       sync.Mutex
	closed       chan struct{}
	closeOnce    sync.Once
	readDeadline time.Time
}

func newKCPPacketConn(writeFunc func(payload []byte) error, remoteAddr net.Addr, localAddr net.Addr) *kcpPacketConn {
	return &kcpPacketConn{
		writeFunc:  writeFunc,
		remoteAddr: remoteAddr,
		localAddr:  localAddr,
		readCh:     make(chan []byte, 256),
		closed:     make(chan struct{}),
	}
}

func (c *kcpPacketConn) push(payload []byte) bool {
	copyOfPayload := make([]byte, len(payload))
	copy(copyOfPayload, payload)
	select {
	case c.readCh <- copyOfPayload:
		return true
	case <-c.closed:
		return false
	default:
		// The KCP session is not draining fast enough; dropping is safe, the
		// ARQ layer retransmits.
		return false
	}
}

func (c *kcpPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, os.ErrClosed
	default:
	}
	err := c.writeFunc(p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *kcpPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.access.Lock()
		deadline := c.readDeadline
		c.access.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case payload := <-c.readCh:
			if timer != nil {
				timer.Stop()
			}
			if len(payload) > len(p) {
				// KCP has no checksum of its own, so handing it a truncated
				// segment would silently corrupt the stream. Dropping is safe:
				// the ARQ layer retransmits.
				continue
			}
			n := copy(p, payload)
			return n, c.remoteAddr, nil
		case <-c.closed:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, os.ErrClosed
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		}
	}
}

func (c *kcpPacketConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
	})
	return nil
}

func (c *kcpPacketConn) LocalAddr() net.Addr {
	if c.localAddr == nil {
		return &net.UDPAddr{}
	}
	return c.localAddr
}

func (c *kcpPacketConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *kcpPacketConn) SetReadDeadline(t time.Time) error {
	c.access.Lock()
	c.readDeadline = t
	c.access.Unlock()
	return nil
}

func (c *kcpPacketConn) SetWriteDeadline(t time.Time) error {
	// The socket underneath is shared by every session, so a write deadline
	// there would tear down unrelated traffic. KCP buffers writes internally,
	// so there is nothing to time out here.
	return nil
}

// kcpSession is the client side of the reliable channel: one KCP session
// carrying one smux session, so that every proxied TCP connection becomes a
// cheap stream instead of a fresh handshake.
type kcpSession struct {
	access  sync.Mutex
	conv    uint32
	virtual *kcpPacketConn
	arq     *kcp.UDPSession
	mux     *smux.Session
	closed  atomic.Bool
}

func newConv() (uint32, error) {
	var buf [4]byte
	_, err := io.ReadFull(rand.Reader, buf[:])
	if err != nil {
		return 0, E.Cause(err, "generate conversation id")
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func (s *kcpSession) openStream() (*smux.Stream, error) {
	s.access.Lock()
	muxSession := s.mux
	s.access.Unlock()
	if muxSession == nil {
		return nil, os.ErrClosed
	}
	return muxSession.OpenStream()
}

func (s *kcpSession) close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.access.Lock()
	muxSession := s.mux
	arq := s.arq
	s.access.Unlock()
	if muxSession != nil {
		muxSession.Close()
	}
	if arq != nil {
		arq.Close()
	}
	if s.virtual != nil {
		s.virtual.Close()
	}
}

// serverKCPChannel is one accepted reliable channel on the server. KCP
// conversation ids are picked by the client, so the server only has to demux
// decrypted segments by (instance, conversation) and feed them to the matching
// session.
type serverKCPChannel struct {
	instanceID [instanceIDSize]byte
	conv       uint32
	clientAddr M.Socksaddr

	virtual  *kcpPacketConn
	arq      *kcp.UDPSession
	mux      *smux.Session
	lastSeen atomic.Int64
	closed   atomic.Bool
}

func newServerKCPChannel(instanceID [instanceIDSize]byte, conv uint32, clientAddr M.Socksaddr, writeFunc func(payload []byte) error, localAddr net.Addr, kcpOptions kcpConfig) (*serverKCPChannel, error) {
	channel := &serverKCPChannel{
		instanceID: instanceID,
		conv:       conv,
		clientAddr: clientAddr,
	}
	channel.virtual = newKCPPacketConn(writeFunc, clientAddr.UDPAddr(), localAddr)
	arq, err := kcp.NewConn3(conv, clientAddr.UDPAddr(), nil, 0, 0, channel.virtual)
	if err != nil {
		channel.virtual.Close()
		return nil, E.Cause(err, "create kcp session")
	}
	channel.arq = arq
	kcpOptions.apply(arq)
	muxSession, err := smux.Server(arq, kcpOptions.smuxConfig())
	if err != nil {
		arq.Close()
		channel.virtual.Close()
		return nil, E.Cause(err, "create smux session")
	}
	channel.mux = muxSession
	channel.lastSeen.Store(time.Now().UnixNano())
	return channel, nil
}

func (c *serverKCPChannel) touch() {
	c.lastSeen.Store(time.Now().UnixNano())
}

func (c *serverKCPChannel) idleFor() time.Duration {
	return time.Since(time.Unix(0, c.lastSeen.Load()))
}

func (c *serverKCPChannel) close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	if c.mux != nil {
		c.mux.Close()
	}
	if c.arq != nil {
		c.arq.Close()
	}
	if c.virtual != nil {
		c.virtual.Close()
	}
}

// serveStreams accepts multiplexed streams and hands each one to the callback,
// which is expected to read the destination address header and route it.
func (c *serverKCPChannel) serveStreams(logger logger.ContextLogger, ctx context.Context, handle func(stream *smux.Stream)) {
	for {
		stream, err := c.mux.AcceptStream()
		if err != nil {
			if !c.closed.Load() {
				logger.Debug("uss: kcp channel ", c.conv, " from ", c.clientAddr, " closed: ", err)
			}
			return
		}
		c.touch()
		go handle(stream)
	}
}
