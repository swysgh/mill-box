package ush

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swysgh/mill-box/adapter/outbound"
	C "github.com/swysgh/mill-box/constant"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/bufio/deadline"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"

	"golang.org/x/crypto/ssh"
)

// directDialer is the transport dialer used by the outbound in these tests: the
// sshd stand-in runs on loopback, so a plain TCP dial is enough.
type directDialer struct{}

func (d *directDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, destination.String())
}

func (d *directDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

// sshTestServer is an sshd stand-in: it authenticates with a password and serves
// every direct-tcpip channel by dialing the requested address, exactly like
// OpenSSH's forwarding would. It also accounts for channels so tests can detect
// channels the client never closes.
type sshTestServer struct {
	listener net.Listener
	config   *ssh.ServerConfig

	opened atomic.Int64
	closed atomic.Int64
	active atomic.Int64
}

type directTCPIPRequest struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func newSSHTestServer(t *testing.T) *sshTestServer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	server := &sshTestServer{
		config: &ssh.ServerConfig{
			PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if metadata.User() == "test" && string(password) == "test" {
					return nil, nil
				}
				return nil, errors.New("denied")
			},
		},
	}
	server.config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.listener = listener
	go server.serve()
	t.Cleanup(func() { listener.Close() })
	return server
}

func (s *sshTestServer) addr() string {
	return s.listener.Addr().String()
}

func (s *sshTestServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *sshTestServer) handleConn(raw net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(raw, s.config)
	if err != nil {
		raw.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "direct-tcpip" {
			newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		var request directTCPIPRequest
		err = ssh.Unmarshal(newChannel.ExtraData(), &request)
		if err != nil {
			newChannel.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(channelRequests)
		s.opened.Add(1)
		s.active.Add(1)
		go func() {
			defer func() {
				channel.Close()
				s.active.Add(-1)
				s.closed.Add(1)
			}()
			target, err := net.DialTimeout("tcp", net.JoinHostPort(request.Host, strconv.Itoa(int(request.Port))), 5*time.Second)
			if err != nil {
				return
			}
			var waitGroup sync.WaitGroup
			waitGroup.Add(2)
			go func() {
				defer waitGroup.Done()
				io.Copy(target, channel)
				target.Close()
			}()
			go func() {
				defer waitGroup.Done()
				io.Copy(channel, target)
				target.Close()
			}()
			waitGroup.Wait()
		}()
	}
}

// uotEchoServer mirrors the server half of ush (protocol/ush/inbound.go): every
// accepted connection is a UoT stream, the first bytes are a UoT request, and
// the stream then carries datagrams. It answers every datagram with the same
// payload, i.e. it plays the role of a UDP echo server at the destination.
type uotEchoServer struct {
	listener net.Listener
	streams  atomic.Int64
}

func newUoTEchoServer(t *testing.T) *uotEchoServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &uotEchoServer{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.streams.Add(1)
			go server.handleConn(conn)
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return server
}

func (s *uotEchoServer) addr() string {
	return s.listener.Addr().String()
}

func (s *uotEchoServer) handleConn(conn net.Conn) {
	defer conn.Close()
	request, err := uot.ReadRequest(conn)
	if err != nil {
		return
	}
	uConn := uot.NewConn(conn, *request)
	for {
		buffer := buf.NewPacket()
		destination, err := uConn.ReadPacket(buffer)
		if err != nil {
			buffer.Release()
			return
		}
		// WritePacket takes ownership of the buffer.
		err = uConn.WritePacket(buffer, destination)
		if err != nil {
			return
		}
	}
}

// blackholeServer accepts TCP connections and never sends anything, like a
// server that completes the TCP handshake but never answers the protocol
// handshake.
type blackholeServer struct {
	listener net.Listener
}

func newBlackholeServer(t *testing.T) *blackholeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &blackholeServer{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(io.Discard, conn)
				conn.Close()
			}()
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return server
}

func (s *blackholeServer) addr() string {
	return s.listener.Addr().String()
}

func newTestOutbound(t *testing.T, sshAddress string, uotAddress string) *Outbound {
	t.Helper()
	outboundInstance := &Outbound{
		Adapter:       outbound.NewAdapter(C.TypeUSH, "test-ush", []string{N.NetworkTCP, N.NetworkUDP}, nil),
		logger:        logger.NOP(),
		dialer:        &directDialer{},
		serverAddr:    M.ParseSocksaddr(sshAddress),
		user:          "test",
		authMethod:    []ssh.AuthMethod{ssh.Password("test")},
		clientVersion: "SSH-2.0-ush_test",
		uotServerAddr: M.ParseSocksaddr(uotAddress),
	}
	outboundInstance.uotClient = &uot.Client{
		Dialer:  &uotServerDialer{outbound: outboundInstance},
		Version: uot.Version,
	}
	return outboundInstance
}

func udpEcho(conn net.Conn, payload []byte) ([]byte, error) {
	_, err := conn.Write(payload)
	if err != nil {
		return nil, err
	}
	type readResult struct {
		data []byte
		err  error
	}
	results := make(chan readResult, 1)
	go func() {
		response := make([]byte, 65535)
		n, readErr := conn.Read(response)
		results <- readResult{response[:n], readErr}
	}()
	select {
	case result := <-results:
		return result.data, result.err
	case <-time.After(5 * time.Second):
		return nil, errors.New("udp exchange timed out")
	}
}

func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// TestUSHUDPConnectRoundTrip covers the connect-style UDP path
// (Outbound.DialContext -> uot.Client.DialContext).
func TestUSHUDPConnectRoundTrip(t *testing.T) {
	sshServer := newSSHTestServer(t)
	echoServer := newUoTEchoServer(t)
	outboundInstance := newTestOutbound(t, sshServer.addr(), echoServer.addr())
	destination := M.ParseSocksaddr("127.0.0.1:5353")

	conn, err := outboundInstance.DialContext(context.Background(), N.NetworkUDP, destination)
	if err != nil {
		t.Fatal("dial udp: ", err)
	}
	defer conn.Close()

	payload := []byte("connect-mode-payload")
	response, err := udpEcho(conn, payload)
	if err != nil {
		t.Fatal("udp exchange: ", err)
	}
	if string(response) != string(payload) {
		t.Fatalf("unexpected response %q", response)
	}
}

// TestUSHUDPListenPacketRoundTrip covers the NAT-style UDP path
// (Outbound.ListenPacket -> uot.Client.ListenPacket), which carries a per-packet
// destination address.
func TestUSHUDPListenPacketRoundTrip(t *testing.T) {
	sshServer := newSSHTestServer(t)
	echoServer := newUoTEchoServer(t)
	outboundInstance := newTestOutbound(t, sshServer.addr(), echoServer.addr())
	destination := M.ParseSocksaddr("127.0.0.1:5353")

	packetConn, err := outboundInstance.ListenPacket(context.Background(), destination)
	if err != nil {
		t.Fatal("listen packet: ", err)
	}
	defer packetConn.Close()

	payload := []byte("nat-mode-payload")
	_, err = packetConn.WriteTo(payload, destination.UDPAddr())
	if err != nil {
		t.Fatal("write packet: ", err)
	}
	response := make([]byte, 65535)
	_ = packetConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, addr, err := packetConn.ReadFrom(response)
	if err != nil {
		t.Fatal("read packet: ", err)
	}
	if string(response[:n]) != string(payload) {
		t.Fatalf("unexpected response %q", response[:n])
	}
	if addr == nil || addr.String() != destination.UDPAddr().String() {
		t.Fatalf("unexpected source address %v, want %v", addr, destination.UDPAddr())
	}
}

// TestUSHUDPChannelReclaim opens many short-lived UDP flows (the DNS pattern) and
// requires every SSH channel to be closed and the stream counter to return to
// zero once the flows are gone.
func TestUSHUDPChannelReclaim(t *testing.T) {
	sshServer := newSSHTestServer(t)
	echoServer := newUoTEchoServer(t)
	outboundInstance := newTestOutbound(t, sshServer.addr(), echoServer.addr())
	destination := M.ParseSocksaddr("127.0.0.1:5353")

	const flowCount = 30
	for index := 0; index < flowCount; index++ {
		conn, err := outboundInstance.DialContext(context.Background(), N.NetworkUDP, destination)
		if err != nil {
			t.Fatal("dial udp: ", err)
		}
		payload := []byte("flow-" + strconv.Itoa(index))
		response, err := udpEcho(conn, payload)
		if err != nil {
			conn.Close()
			t.Fatal("udp exchange: ", err)
		}
		if string(response) != string(payload) {
			conn.Close()
			t.Fatalf("unexpected response %q", response)
		}
		err = conn.Close()
		if err != nil {
			t.Fatal("close: ", err)
		}
	}

	if opened := sshServer.opened.Load(); opened != flowCount {
		t.Fatalf("ssh channels opened = %d, want %d", opened, flowCount)
	}
	waitFor(t, "all ssh channels to be closed", func() bool {
		return sshServer.active.Load() == 0
	})
	outboundInstance.clientAccess.Lock()
	streams := outboundInstance.streams
	outboundInstance.clientAccess.Unlock()
	if streams != 0 {
		t.Fatalf("outbound streams = %d, want 0", streams)
	}
	if echoStreams := echoServer.streams.Load(); echoStreams != flowCount {
		t.Fatalf("uot streams accepted = %d, want %d", echoStreams, flowCount)
	}
}

// TestUSHUDPConnectSourceAddress follows route/conn.go's UDP-connect branch
// (metadata.UDPConnect, i.e. a rule with "udp_connect": true): the outbound
// connection is wrapped with bufio.NewUnbindPacketConn, which captures
// conn.RemoteAddr() as the source address of every received datagram.
func TestUSHUDPConnectSourceAddress(t *testing.T) {
	sshServer := newSSHTestServer(t)
	echoServer := newUoTEchoServer(t)
	outboundInstance := newTestOutbound(t, sshServer.addr(), echoServer.addr())
	destination := M.ParseSocksaddr("10.99.0.1:5353")

	remoteConn, err := outboundInstance.DialContext(context.Background(), N.NetworkUDP, destination)
	if err != nil {
		t.Fatal("dial udp: ", err)
	}
	defer remoteConn.Close()

	// route/conn.go:219-223
	remotePacketConn := bufio.NewUnbindPacketConn(remoteConn)
	var destinationAddress netip.Addr
	connRemoteAddr := M.AddrFromNet(remoteConn.RemoteAddr())
	if connRemoteAddr != destination.Addr {
		destinationAddress = connRemoteAddr
	}
	// route/conn.go:269-282
	if destinationAddress.IsValid() {
		remotePacketConn = bufio.NewNATPacketConn(
			bufio.NewPacketConn(remotePacketConn),
			M.SocksaddrFrom(destinationAddress, destination.Port),
			destination,
		)
	}

	payload := []byte("udp-connect-payload")
	request := buf.NewPacket()
	request.Write(payload)
	err = remotePacketConn.WritePacket(request, destination)
	if err != nil {
		t.Fatal("write packet: ", err)
	}
	response := buf.NewPacket()
	defer response.Release()
	source, err := remotePacketConn.ReadPacket(response)
	if err != nil {
		t.Fatal("read packet: ", err)
	}
	if string(response.Bytes()) != string(payload) {
		t.Fatalf("unexpected response %q", response.Bytes())
	}
	if source != destination {
		t.Fatalf("received datagram reported from %v, want %v", source, destination)
	}
}

// TestUSHConnectionDeadlineContract follows dns/transport/tcp.go's
// setConnDeadline: a caller asks the connection whether it needs an additional
// read deadline layer and, when it does, bounds the exchange by closing the
// connection on context cancellation. The SSH channel cannot carry a deadline,
// so the connection has to declare that; otherwise the caller keeps a deadline
// that silently never fires.
func TestUSHConnectionDeadlineContract(t *testing.T) {
	sshServer := newSSHTestServer(t)
	blackhole := newBlackholeServer(t)
	outboundInstance := newTestOutbound(t, sshServer.addr(), sshServer.addr())

	conn, err := outboundInstance.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr(blackhole.addr()))
	if err != nil {
		t.Fatal("dial tcp: ", err)
	}
	defer conn.Close()

	if !deadline.NeedAdditionalReadDeadline(conn) {
		t.Fatal("the connection does not declare that it needs an additional read deadline: callers keep a deadline that never fires")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		conn.Close()
	})
	defer stop()
	started := time.Now()
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("read from a silent server returned without an error")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("the read took %v: the caller's fallback never fired", time.Since(started))
	}
}

// TestUSHUDPReadDeadline covers the framework's contract for the UoT connection:
// the connection itself cannot carry a deadline, but it must declare that, so a
// caller that adds a deadline layer gets a working read timeout on an idle UDP
// flow instead of blocking forever.
func TestUSHUDPReadDeadline(t *testing.T) {
	sshServer := newSSHTestServer(t)
	echoServer := newUoTEchoServer(t)
	outboundInstance := newTestOutbound(t, sshServer.addr(), echoServer.addr())
	destination := M.ParseSocksaddr("127.0.0.1:5353")

	conn, err := outboundInstance.DialContext(context.Background(), N.NetworkUDP, destination)
	if err != nil {
		t.Fatal("dial udp: ", err)
	}
	defer conn.Close()

	if !deadline.NeedAdditionalReadDeadline(conn) {
		t.Fatal("the UDP connection does not declare that it needs an additional read deadline")
	}
	deadlineConn := deadline.NewConn(conn)
	err = deadlineConn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if err != nil {
		t.Fatal("set read deadline: ", err)
	}
	_, err = deadlineConn.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read after deadline: %v, want %v", err, os.ErrDeadlineExceeded)
	}
}
