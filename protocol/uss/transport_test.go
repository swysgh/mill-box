package uss

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/swysgh/mill-box/adapter"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var testPSK = bytes.Repeat([]byte{0x5a}, keySize)

func testMTUConfigFor(transport string) mtuConfig {
	return mtuConfig{
		enabled:       false,
		base:          1200,
		max:           1200,
		step:          8,
		probeInterval: time.Hour,
		probeTimeout:  time.Second,
		probeAttempts: 1,
	}
}

func testServerConfig() serverConfig {
	return serverConfig{
		psk:              testPSK,
		tcpEnabled:       true,
		udpEnabled:       true,
		kcp:              defaultKCPConfig(),
		mtu:              testMTUConfigFor("server"),
		sessionTimeout:   time.Minute,
		handshakeTimeout: 10 * time.Second,
		udpTimeout:       time.Minute,
		maxSessions:      8,
		maxUDPFlows:      64,
		maxKCPChannels:   8,
		replayWindowSize: 1024,
	}
}

func testClientConfig(server M.Socksaddr) clientConfig {
	return clientConfig{
		server:           server,
		psk:              testPSK,
		tcpEnabled:       true,
		kcp:              defaultKCPConfig(),
		mtu:              testMTUConfigFor("client"),
		pingInterval:     30 * time.Second,
		udpTimeout:       time.Minute,
		maxUDPFlows:      64,
		replayWindowSize: 1024,
	}
}

// testPacketWriter stands in for the inbound listener's packet writer.
type testPacketWriter struct {
	conn *net.UDPConn
}

func (w *testPacketWriter) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	defer buffer.Release()
	_, err := w.conn.WriteToUDP(buffer.Bytes(), destination.UDPAddr())
	return err
}

// testRouter relays every flow to the real destination, which is all the router
// has to do for these tests.
type testRouter struct {
	t *testing.T
}

func (r *testRouter) RouteConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	remote, err := net.DialTimeout("tcp", metadata.Destination.String(), 5*time.Second)
	if err != nil {
		return err
	}
	defer remote.Close()
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(remote, conn)
		done <- err
	}()
	go func() {
		_, err := io.Copy(conn, remote)
		done <- err
	}()
	err = <-done
	conn.Close()
	return err
}

func (r *testRouter) RoutePacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	remote, err := net.DialUDP("udp", nil, metadata.Destination.UDPAddr())
	if err != nil {
		return err
	}
	defer remote.Close()
	done := make(chan error, 2)
	go func() {
		buffer := buf.NewPacket()
		defer buffer.Release()
		for {
			buffer.Reset()
			_, err := conn.ReadPacket(buffer)
			if err != nil {
				done <- err
				return
			}
			_, err = remote.Write(buffer.Bytes())
			if err != nil {
				done <- err
				return
			}
		}
	}()
	go func() {
		payload := make([]byte, 65535)
		for {
			n, err := remote.Read(payload)
			if err != nil {
				done <- err
				return
			}
			buffer := buf.NewSize(n)
			_, _ = buffer.Write(payload[:n])
			err = conn.WritePacket(buffer, metadata.Destination)
			if err != nil {
				done <- err
				return
			}
		}
	}()
	err = <-done
	conn.Close()
	return err
}

func (r *testRouter) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	err := r.RouteConnection(ctx, conn, metadata)
	if onClose != nil {
		onClose(err)
	}
}

func (r *testRouter) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	err := r.RoutePacketConnection(ctx, conn, metadata)
	if onClose != nil {
		onClose(err)
	}
}

type testServer struct {
	conn      *net.UDPConn
	transport *serverTransport
	cancel    context.CancelFunc
}

func startTestServer(t *testing.T, config serverConfig) *testServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	transport, err := newServerTransport(ctx, logger.NOP(), config, &testRouter{t: t}, "uss-test", &testPacketWriter{conn: conn}, conn.LocalAddr())
	if err != nil {
		conn.Close()
		cancel()
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, maxDatagramSize)
		for {
			n, source, err := conn.ReadFromUDPAddrPort(buffer)
			if err != nil {
				return
			}
			transport.handlePacket(buffer[:n], M.SocksaddrFromNetIP(source).Unwrap())
		}
	}()
	server := &testServer{conn: conn, transport: transport, cancel: cancel}
	t.Cleanup(server.close)
	return server
}

func (s *testServer) close() {
	s.transport.close()
	s.conn.Close()
	s.cancel()
}

func (s *testServer) addr() M.Socksaddr {
	return M.SocksaddrFromNet(s.conn.LocalAddr()).Unwrap()
}

func (s *testServer) session(instanceID [instanceIDSize]byte) *serverSession {
	s.transport.access.Lock()
	defer s.transport.access.Unlock()
	return s.transport.sessions[instanceID]
}

func (s *testServer) sessionCount() int {
	s.transport.access.Lock()
	defer s.transport.access.Unlock()
	return len(s.transport.sessions)
}

func startTestClient(t *testing.T, config clientConfig, serverAddr string) *clientTransport {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	transport, err := newClientTransport(ctx, logger.NOP(), config, func(dialContext context.Context) (net.Conn, error) {
		dialer := net.Dialer{}
		return dialer.DialContext(dialContext, "udp", serverAddr)
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		transport.close()
		cancel()
	})
	return transport
}

func startUDPEcho(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		payload := make([]byte, 65535)
		for {
			n, source, err := conn.ReadFromUDP(payload)
			if err != nil {
				return
			}
			_, err = conn.WriteToUDP(payload[:n], source)
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		conn.Close()
	})
	return conn
}

func startTCPEcho(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(conn, conn)
				conn.Close()
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
	})
	return listener
}

func TestTransportRawDatagram(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	client := startTestClient(t, testClientConfig(server.addr()), server.conn.LocalAddr().String())
	echo := startUDPEcho(t)
	echoAddr := M.SocksaddrFromNet(echo.LocalAddr()).Unwrap()

	packetConn := listenTestPacket(t, client)

	payload := []byte("hello uss")
	buffer := buf.NewSize(len(payload))
	_, _ = buffer.Write(payload)
	err := packetConn.WritePacket(buffer, echoAddr)
	if err != nil {
		t.Fatal("write datagram: ", err)
	}
	err = packetConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reply := buf.NewPacket()
	defer reply.Release()
	destination, err := packetConn.ReadPacket(reply)
	if err != nil {
		t.Fatal("read datagram: ", err)
	}
	if !bytes.Equal(reply.Bytes(), payload) {
		t.Fatalf("payload mismatch: got %q", reply.Bytes())
	}
	if destination != echoAddr {
		t.Fatalf("destination mismatch: got %s want %s", destination, echoAddr)
	}
}

func TestTransportFragmentedDatagram(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	client := startTestClient(t, testClientConfig(server.addr()), server.conn.LocalAddr().String())
	echo := startUDPEcho(t)
	echoAddr := M.SocksaddrFromNet(echo.LocalAddr()).Unwrap()

	packetConn := listenTestPacket(t, client)

	// Far beyond the 1200 byte datagram budget configured for this test, so the
	// datagram has to be fragmented and reassembled in both directions.
	payload := make([]byte, 3000)
	for index := range payload {
		payload[index] = byte(index)
	}
	buffer := buf.NewSize(len(payload))
	_, _ = buffer.Write(payload)
	err := packetConn.WritePacket(buffer, echoAddr)
	if err != nil {
		t.Fatal("write datagram: ", err)
	}
	err = packetConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reply := buf.NewPacket()
	defer reply.Release()
	_, err = packetConn.ReadPacket(reply)
	if err != nil {
		t.Fatal("read datagram: ", err)
	}
	if !bytes.Equal(reply.Bytes(), payload) {
		t.Fatalf("reassembled payload mismatch: got %d bytes want %d", reply.Len(), len(payload))
	}
}

func TestTransportMultipleDestinations(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	client := startTestClient(t, testClientConfig(server.addr()), server.conn.LocalAddr().String())
	first := startUDPEcho(t)
	second := startUDPEcho(t)
	firstAddr := M.SocksaddrFromNet(first.LocalAddr()).Unwrap()
	secondAddr := M.SocksaddrFromNet(second.LocalAddr()).Unwrap()

	packetConn := listenTestPacket(t, client)

	// One packet connection carries datagrams to two different destinations,
	// which is what a tun inbound produces.
	for _, testCase := range []struct {
		destination M.Socksaddr
		payload     []byte
	}{
		{firstAddr, []byte("to-first")},
		{secondAddr, []byte("to-second")},
		{firstAddr, []byte("to-first-again")},
	} {
		buffer := buf.NewSize(len(testCase.payload))
		_, _ = buffer.Write(testCase.payload)
		err := packetConn.WritePacket(buffer, testCase.destination)
		if err != nil {
			t.Fatal("write datagram: ", err)
		}
		err = packetConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		reply := buf.NewPacket()
		destination, err := packetConn.ReadPacket(reply)
		if err != nil {
			reply.Release()
			t.Fatal("read datagram: ", err)
		}
		if destination != testCase.destination {
			reply.Release()
			t.Fatalf("reply came from %s, expected %s", destination, testCase.destination)
		}
		if !bytes.Equal(reply.Bytes(), testCase.payload) {
			got := reply.Bytes()
			reply.Release()
			t.Fatalf("payload mismatch: got %q", got)
		}
		reply.Release()
	}
}

func TestTransportReliableStream(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	client := startTestClient(t, testClientConfig(server.addr()), server.conn.LocalAddr().String())
	echo := startTCPEcho(t)
	echoAddr := M.SocksaddrFromNet(echo.Addr()).Unwrap()

	session, err := client.kcp()
	if err != nil {
		t.Fatal(err)
	}
	stream, err := session.openStream()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	err = M.SocksaddrSerializer.WriteAddrPort(stream, echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 40000)
	for index := range payload {
		payload[index] = byte(index * 7)
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := stream.Write(payload)
		writeDone <- err
	}()
	err = stream.SetReadDeadline(time.Now().Add(20 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	_, err = io.ReadFull(stream, reply)
	if err != nil {
		t.Fatal("read stream: ", err)
	}
	if err = <-writeDone; err != nil {
		t.Fatal("write stream: ", err)
	}
	if !bytes.Equal(reply, payload) {
		t.Fatal("stream payload mismatch")
	}
}

func TestTransportReliableStreamReusesChannel(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	client := startTestClient(t, testClientConfig(server.addr()), server.conn.LocalAddr().String())
	echo := startTCPEcho(t)
	echoAddr := M.SocksaddrFromNet(echo.Addr()).Unwrap()

	first, err := client.kcp()
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.kcp()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("each dial created a new reliable channel instead of reusing the shared one")
	}
	for index := 0; index < 3; index++ {
		stream, err := first.openStream()
		if err != nil {
			t.Fatal(err)
		}
		err = M.SocksaddrSerializer.WriteAddrPort(stream, echoAddr)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("stream-payload")
		_, err = stream.Write(payload)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
		reply := make([]byte, len(payload))
		_, err = io.ReadFull(stream, reply)
		if err != nil {
			t.Fatal("read stream: ", err)
		}
		if !bytes.Equal(reply, payload) {
			t.Fatal("stream payload mismatch")
		}
		stream.Close()
	}
	session := server.session(instanceIDOf(t, client))
	if session == nil {
		t.Fatal("server lost the session")
	}
	if len(session.kcpChannels) != 1 {
		t.Fatalf("expected one server side channel, got %d", len(session.kcpChannels))
	}
}

// listenTestPacket mirrors what the outbound hands to the router for a UDP
// flow.
func listenTestPacket(t *testing.T, transport *clientTransport) N.PacketConn {
	t.Helper()
	conn := newClientUDPSession(context.Background(), transport, logger.NOP())
	t.Cleanup(func() {
		conn.Close()
	})
	return conn
}

func instanceIDOf(t *testing.T, client *clientTransport) [instanceIDSize]byte {
	t.Helper()
	return client.instanceID()
}

func TestServerRejectsReplay(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	var instance [instanceIDSize]byte
	copy(instance[:], "replay-1")
	keys, err := deriveSessionKeys(testPSK, instance, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("udp", server.conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	// A ping is answered with a pong, which makes "the server processed this
	// datagram" observable from the outside.
	datagram := keys.seal(nil, instance, 11, []byte{packetTypePing})
	_, err = raw.Write(datagram)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maxDatagramSize)
	err = raw.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Read(buffer); err != nil {
		t.Fatal("first datagram was not answered: ", err)
	}
	// The very same datagram must now be dropped.
	_, err = raw.Write(datagram)
	if err != nil {
		t.Fatal(err)
	}
	err = raw.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Read(buffer)
	if err == nil {
		t.Fatal("replayed datagram was processed again")
	}
	var netError net.Error
	if !errors.As(err, &netError) || !netError.Timeout() {
		t.Fatalf("expected a read timeout, got %v", err)
	}
}

func TestServerRejectsWrongKey(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	var instance [instanceIDSize]byte
	copy(instance[:], "wrongkey")
	keys, err := deriveSessionKeys(bytes.Repeat([]byte{0x01}, keySize), instance, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("udp", server.conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_, err = raw.Write(keys.seal(nil, instance, 0, []byte{packetTypePing}))
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maxDatagramSize)
	err = raw.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Read(buffer); err == nil {
		t.Fatal("server answered a datagram it could not authenticate")
	}
	if server.sessionCount() != 0 {
		t.Fatalf("unauthenticated datagram created %d sessions", server.sessionCount())
	}
}

func TestServerRejectsGarbage(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	raw, err := net.Dial("udp", server.conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, payload := range [][]byte{
		{},
		{0x01},
		bytes.Repeat([]byte{0xff}, 64),
		bytes.Repeat([]byte{0xff}, maxDatagramSize),
	} {
		if _, err = raw.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if server.sessionCount() != 0 {
		t.Fatalf("garbage created %d sessions", server.sessionCount())
	}
}

func TestServerProbeAck(t *testing.T) {
	config := testServerConfig()
	// A ceiling above the probed size, so that passive observation has room to
	// move the estimate.
	config.mtu.max = 1500
	server := startTestServer(t, config)
	var instance [instanceIDSize]byte
	copy(instance[:], "probe-01")
	keys, err := deriveSessionKeys(testPSK, instance, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("udp", server.conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// A probe padded to 1400 bytes must come back acknowledged, and the server
	// must remember that this size fits. The acknowledgement travels on the
	// server-to-client key, which is the receiving half of this key pair.
	body := make([]byte, 1400-cleartextSize-aeadTagSize-1)
	body[0] = 0x12
	body[1] = 0x34
	body[2] = 0x56
	body[3] = 0x78
	_, err = raw.Write(keys.seal(nil, instance, 0, append([]byte{packetTypeProbe}, body...)))
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maxDatagramSize)
	err = raw.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	n, err := raw.Read(buffer)
	if err != nil {
		t.Fatal("probe was not acknowledged: ", err)
	}
	plaintext, err := keys.open(buffer[:n])
	if err != nil {
		t.Fatal("could not decrypt acknowledgement: ", err)
	}
	if len(plaintext) != 1+probeHeaderSize || plaintext[0] != packetTypeProbeAck {
		t.Fatalf("unexpected acknowledgement: %v", plaintext)
	}
	if plaintext[1] != 0x12 || plaintext[2] != 0x34 || plaintext[3] != 0x56 || plaintext[4] != 0x78 {
		t.Fatalf("probe id not echoed: %v", plaintext[1:])
	}
	session := server.session(instance)
	if session == nil {
		t.Fatal("probe did not create a session")
	}
	if got := session.pathMTUSize(); got != 1400 {
		t.Fatalf("server did not learn the probed size: %d", got)
	}
}

func TestKCPPacketConnDropsOversizedSegment(t *testing.T) {
	conn := newKCPPacketConn(func(payload []byte) error { return nil }, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}, &net.UDPAddr{})
	defer conn.Close()
	// KCP has no checksum of its own, so a segment that does not fit must be
	// dropped rather than handed over truncated.
	if !conn.push(bytes.Repeat([]byte{0xaa}, 4000)) {
		t.Fatal("push rejected")
	}
	if !conn.push([]byte("second")) {
		t.Fatal("push rejected")
	}
	buffer := make([]byte, 1500)
	n, _, err := conn.ReadFrom(buffer)
	if err != nil {
		t.Fatal("read: ", err)
	}
	if string(buffer[:n]) != "second" {
		t.Fatalf("oversized segment was not dropped: got %d bytes", n)
	}
}

func TestTransportRedialsAfterSocketLoss(t *testing.T) {
	server := startTestServer(t, testServerConfig())
	client := startTestClient(t, testClientConfig(server.addr()), server.conn.LocalAddr().String())
	echo := startUDPEcho(t)
	echoAddr := M.SocksaddrFromNet(echo.LocalAddr()).Unwrap()
	packetConn := listenTestPacket(t, client)

	roundTrip := func(payload []byte) {
		t.Helper()
		buffer := buf.NewSize(len(payload))
		_, _ = buffer.Write(payload)
		err := packetConn.WritePacket(buffer, echoAddr)
		if err != nil {
			t.Fatal("write datagram: ", err)
		}
		if err = packetConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		reply := buf.NewPacket()
		defer reply.Release()
		if _, err = packetConn.ReadPacket(reply); err != nil {
			t.Fatal("read datagram: ", err)
		}
		if !bytes.Equal(reply.Bytes(), payload) {
			t.Fatalf("payload mismatch: got %q", reply.Bytes())
		}
	}

	roundTrip([]byte("before"))
	// Simulate the fatal read error path: the socket is gone but the outbound
	// must keep working by dialling a new one.
	client.dropConn(client.currentConn())
	roundTrip([]byte("after"))
}
