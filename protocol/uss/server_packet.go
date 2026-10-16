package uss

import (
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/swysgh/mill-box/adapter"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/log"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// serverFlowConn carries the datagrams of one (client flow id, destination)
// pair. Routing each flow separately is what lets route rules, dns and the
// outbound selection apply per destination exactly like they do for a locally
// originated packet connection.
type serverFlowConn struct {
	transport *serverTransport
	session   *serverSession
	key       flowKey

	readCh chan []byte
	done   chan struct{}

	// A read that is already waiting has to notice a new deadline, otherwise
	// SetReadDeadline could never interrupt it.
	deadlineChanged chan struct{}

	fragmentSeq atomic.Uint32
	lastActive  atomic.Int64

	deadlineAccess sync.Mutex
	readDeadline   time.Time

	closeOnce sync.Once
}

var _ N.PacketConn = (*serverFlowConn)(nil)

func newServerFlowConn(transport *serverTransport, session *serverSession, key flowKey) *serverFlowConn {
	conn := &serverFlowConn{
		transport:       transport,
		session:         session,
		key:             key,
		readCh:          make(chan []byte, readQueueSize),
		done:            make(chan struct{}),
		deadlineChanged: make(chan struct{}, 1),
	}
	conn.lastActive.Store(time.Now().UnixNano())
	return conn
}

// run routes the flow and blocks until it is closed, then releases the mapping.
func (c *serverFlowConn) run() {
	ctx := log.ContextWithNewID(c.transport.ctx)
	metadata := adapter.InboundContext{
		Inbound:     c.transport.inboundTag,
		InboundType: C.TypeUss,
		Source:      c.session.clientAddr.Load(),
		Destination: c.key.destination,
		Network:     N.NetworkUDP,
		// Hand the router the configured idle timeout so the flow is actually
		// reclaimed after it instead of after the global default.
		UDPTimeout: c.transport.config.udpTimeout,
	}
	c.transport.logger.InfoContext(ctx, "inbound packet connection to ", c.key.destination)
	err := c.transport.router.RoutePacketConnection(adapter.WithContext(ctx, &metadata), c, metadata)
	if err != nil {
		c.transport.logger.DebugContext(ctx, "packet connection to ", c.key.destination, " closed: ", err)
	}
	c.Close()
	c.session.removeFlow(c.key, c)
}

// deliver queues one inbound datagram for the reader.
func (c *serverFlowConn) deliver(payload []byte) {
	c.lastActive.Store(time.Now().UnixNano())
	select {
	case c.readCh <- payload:
	default:
		c.transport.logger.Debug("uss: dropping datagram to ", c.key.destination, ": queue full")
	}
}

func (c *serverFlowConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	payload, err := c.readPayload()
	if err != nil {
		return M.Socksaddr{}, err
	}
	_, err = buffer.Write(payload)
	if err != nil {
		return M.Socksaddr{}, err
	}
	return c.key.destination, nil
}

func (c *serverFlowConn) ReadFrom(p []byte) (int, net.Addr, error) {
	payload, err := c.readPayload()
	if err != nil {
		return 0, nil, err
	}
	n := copy(p, payload)
	return n, c.key.destination.UDPAddr(), nil
}

func (c *serverFlowConn) readPayload() ([]byte, error) {
	for {
		c.deadlineAccess.Lock()
		deadline := c.readDeadline
		c.deadlineAccess.Unlock()
		var timeout <-chan time.Time
		var timer *time.Timer
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case payload := <-c.readCh:
			if timer != nil {
				timer.Stop()
			}
			return payload, nil
		case <-c.done:
			if timer != nil {
				timer.Stop()
			}
			return nil, os.ErrClosed
		case <-timeout:
			return nil, os.ErrDeadlineExceeded
		case <-c.deadlineChanged:
			if timer != nil {
				timer.Stop()
			}
			continue
		}
	}
}

func (c *serverFlowConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	// The caller transfers ownership of the buffer.
	defer buffer.Release()
	if c.session.closed.Load() {
		return os.ErrClosed
	}
	c.lastActive.Store(time.Now().UnixNano())
	payload := buffer.Bytes()
	overhead := 1 + rawHeaderSize
	chunkLimit := c.session.pathMTUSize() - cleartextSize - aeadTagSize - overhead
	if chunkLimit < 1 {
		chunkLimit = 1
	}
	chunks := fragmentPayload(payload, chunkLimit)
	fragmentID := c.fragmentSeq.Add(1)
	for index, chunk := range chunks {
		body := make([]byte, rawHeaderSize+len(chunk))
		writeRawHeader(body, c.key.sessionID, fragmentID, uint8(index), uint8(len(chunks)))
		copy(body[rawHeaderSize:], chunk)
		err := c.transport.sendToClient(c.session, packetTypeUDPReply, body)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *serverFlowConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	copied := buf.NewSize(len(p))
	_, _ = copied.Write(p)
	err := c.WritePacket(copied, M.SocksaddrFromNet(addr))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *serverFlowConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
	})
	return nil
}

func (c *serverFlowConn) LocalAddr() net.Addr {
	if c.transport.localAddr == nil {
		return &net.UDPAddr{}
	}
	return c.transport.localAddr
}

func (c *serverFlowConn) RemoteAddr() net.Addr {
	return c.key.destination.UDPAddr()
}

func (c *serverFlowConn) SetDeadline(t time.Time) error {
	c.deadlineAccess.Lock()
	c.readDeadline = t
	c.deadlineAccess.Unlock()
	select {
	case c.deadlineChanged <- struct{}{}:
	default:
	}
	return nil
}

func (c *serverFlowConn) SetReadDeadline(t time.Time) error {
	return c.SetDeadline(t)
}

func (c *serverFlowConn) SetWriteDeadline(t time.Time) error {
	return nil
}
