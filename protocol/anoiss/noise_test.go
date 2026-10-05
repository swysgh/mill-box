package anoiss

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/sagernet/sing-anytls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

type serverResult struct {
	conn *noiseConn
	peer []byte
	err  error
}

func testKey(t *testing.T) noise.DHKey {
	t.Helper()
	key, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func handshakePair(t *testing.T, clientKey, serverKey noise.DHKey, pin, clientPSK, serverPSK []byte, whitelist map[string]bool) (*noiseConn, *noiseConn, error, error) {
	return handshakePairWithPadding(t, clientKey, serverKey, pin, clientPSK, serverPSK, whitelist, 0, 0, nil)
}

// recordingConn records complete Writes on one side of a net.Pipe. Both sides
// must be wrapped to observe all three XX messages (the client writes only two).
type recordingConn struct {
	net.Conn
	writes *bytes.Buffer
}

func (c *recordingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.writes.Write(b[:n])
	return n, err
}

func handshakePairWithPadding(t *testing.T, clientKey, serverKey noise.DHKey, pin, clientPSK, serverPSK []byte, whitelist map[string]bool, clientPadding, serverPadding int, recorded *[2]bytes.Buffer) (*noiseConn, *noiseConn, error, error) {
	t.Helper()
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { clientRaw.Close(); serverRaw.Close() })
	var clientConn, serverConn net.Conn = clientRaw, serverRaw
	if recorded != nil {
		clientConn = &recordingConn{Conn: clientRaw, writes: &recorded[0]}
		serverConn = &recordingConn{Conn: serverRaw, writes: &recorded[1]}
	}
	serverDone := make(chan serverResult, 1)
	go func() {
		conn, peer, err := ServerHandshake(serverConn, HandshakeOptions{
			StaticKey: serverKey, PreSharedKey: serverPSK, PaddingSize: serverPadding, Timeout: 2 * time.Second,
		})
		if err == nil && whitelist != nil && !(&Inbound{clientPubKeys: whitelist}).clientAllowed(peer) {
			conn.Close()
			err = fmt.Errorf("rejected client with unknown public key: %s", base64.RawURLEncoding.EncodeToString(peer))
		}
		serverDone <- serverResult{conn, peer, err}
	}()
	client, clientErr := ClientHandshake(clientConn, HandshakeOptions{
		StaticKey: clientKey, ExpectedPeer: pin, PreSharedKey: clientPSK, PaddingSize: clientPadding, Timeout: 2 * time.Second,
	})
	server := <-serverDone
	if clientErr == nil && server.err == nil {
		if !bytes.Equal(server.peer, clientKey.Public) {
			t.Fatalf("server peer key mismatch: got %x, want %x", server.peer, clientKey.Public)
		}
		if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := server.conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return client, server.conn, clientErr, server.err
}

func recordedFrames(t *testing.T, recorded *[2]bytes.Buffer) [][]byte {
	t.Helper()
	// XX's on-wire order is client msg1, server msg2, client msg3.
	var frames [2][][]byte
	for side := range recorded {
		data := recorded[side].Bytes()
		for len(data) > 0 {
			if len(data) < headerSize {
				t.Fatalf("side %d: incomplete frame header", side)
			}
			length := int(binary.BigEndian.Uint16(data[:headerSize]))
			data = data[headerSize:]
			if len(data) < length {
				t.Fatalf("side %d: incomplete frame payload", side)
			}
			frames[side] = append(frames[side], data[:length])
			data = data[length:]
		}
	}
	if len(frames[0]) != 2 || len(frames[1]) != 1 {
		t.Fatalf("got %d client frames and %d server frames, want 2 and 1", len(frames[0]), len(frames[1]))
	}
	return [][]byte{frames[0][0], frames[1][0], frames[0][1]}
}

func TestHandshakePaddingUniform(t *testing.T) {
	var recorded [2]bytes.Buffer
	_, _, clientErr, serverErr := handshakePairWithPadding(t, testKey(t), testKey(t), nil, nil, nil, nil, 512, 512, &recorded)
	if clientErr != nil || serverErr != nil {
		t.Fatalf("handshake: client=%v server=%v", clientErr, serverErr)
	}
	frames := recordedFrames(t, &recorded)
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	for i, frame := range frames {
		if len(frame) != 510 {
			t.Errorf("frame %d: payload length %d, want 510", i, len(frame))
		}
	}
	if bytes.Equal(frames[0][xxUnpaddedSize(0):], make([]byte, len(frames[0])-xxUnpaddedSize(0))) {
		t.Fatal("msg1 padding is all zero")
	}
}

func TestHandshakePaddingDisabled(t *testing.T) {
	var recorded [2]bytes.Buffer
	_, _, clientErr, serverErr := handshakePairWithPadding(t, testKey(t), testKey(t), nil, nil, nil, nil, 0, 0, &recorded)
	if clientErr != nil || serverErr != nil {
		t.Fatalf("handshake: client=%v server=%v", clientErr, serverErr)
	}
	frames := recordedFrames(t, &recorded)
	for i, frame := range frames {
		if len(frame) != xxUnpaddedSize(i) {
			t.Errorf("frame %d: payload length %d, want %d", i, len(frame), xxUnpaddedSize(i))
		}
	}
}

func TestHandshakePaddingAsymmetric(t *testing.T) {
	clientKey, serverKey := testKey(t), testKey(t)
	for _, tc := range []struct{ client, server int }{{512, 0}, {512, 256}} {
		t.Run(fmt.Sprintf("client_%d_server_%d", tc.client, tc.server), func(t *testing.T) {
			client, server, clientErr, serverErr := handshakePairWithPadding(t, clientKey, serverKey, nil, nil, nil, nil, tc.client, tc.server, nil)
			if clientErr != nil || serverErr != nil {
				t.Fatalf("handshake: client=%v server=%v", clientErr, serverErr)
			}
			exchange(t, client, server, []byte("client to server"))
			exchange(t, server, client, []byte("server to client"))
		})
	}
}

func TestHandshakePaddingPSK(t *testing.T) {
	psk := bytes.Repeat([]byte{0x51}, 32)
	var recorded [2]bytes.Buffer
	client, server, clientErr, serverErr := handshakePairWithPadding(t, testKey(t), testKey(t), nil, psk, psk, nil, 512, 512, &recorded)
	if clientErr != nil || serverErr != nil {
		t.Fatalf("PSK handshake: client=%v server=%v", clientErr, serverErr)
	}
	for i, frame := range recordedFrames(t, &recorded) {
		if len(frame) != 510 {
			t.Errorf("frame %d: payload length %d, want 510", i, len(frame))
		}
	}
	exchange(t, client, server, []byte("padded PSK works"))
}

func TestHandshakePaddingInvalid(t *testing.T) {
	for _, tc := range []struct{ size, limit int }{{64, 128}, {65536, 65535}} {
		t.Run(fmt.Sprint(tc.size), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			for _, err := range []error{
				func() error { _, err := ClientHandshake(client, HandshakeOptions{PaddingSize: tc.size}); return err }(),
				func() error { _, _, err := ServerHandshake(server, HandshakeOptions{PaddingSize: tc.size}); return err }(),
			} {
				if err == nil || !strings.Contains(err.Error(), fmt.Sprint(tc.size)) || !strings.Contains(err.Error(), fmt.Sprint(tc.limit)) {
					t.Errorf("padding %d: expected error with limit %d, got %v", tc.size, tc.limit, err)
				}
			}
		})
	}
}

func TestHandshakePaddingConfigDefault(t *testing.T) {
	for _, raw := range []string{`{}`, `{"handshake_padding":0}`, `{"handshake_padding":512}`} {
		var inbound option.AnoissInboundOptions
		var outbound option.AnoissOutboundOptions
		if err := json.Unmarshal([]byte(raw), &inbound); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &outbound); err != nil {
			t.Fatal(err)
		}
		want := 512
		if raw == `{"handshake_padding":0}` {
			want = 0
		}
		if got := configuredHandshakePadding(inbound.HandshakePadding); got != want {
			t.Errorf("inbound %s: got %d, want %d", raw, got, want)
		}
		if got := configuredHandshakePadding(outbound.HandshakePadding); got != want {
			t.Errorf("outbound %s: got %d, want %d", raw, got, want)
		}
	}
}

func TestPaddingSchemeValidation(t *testing.T) {
	for _, tc := range []struct {
		name, scheme, position string
	}{
		{"default", string(anytls.DefaultPaddingScheme), ""},
		{"stop_zero", "stop=0", ""},
		{"check_mark", "stop=1\n0=30-30,c", ""},
		{"missing_stop", "0=30-30", "stop"},
		{"bad_stop", "stop=abc", "stop"},
		{"min_zero", "stop=1\n0=0-100", "index 0"},
		{"missing_max", "stop=2\n1=100-", "index 1"},
		{"empty_value", "stop=3\n2=", "index 2"},
		{"bad_range", "stop=4\n3=abc-def", "index 3"},
		{"only_check_mark", "stop=1\n0=c", "index 0"},
		{"bad_index", "stop=1\n01=1-10", "index"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePaddingScheme([]string{tc.scheme})
			if tc.position == "" && err != nil || tc.position != "" && (err == nil || !strings.Contains(err.Error(), tc.position)) {
				t.Fatalf("validatePaddingScheme(%q): got %v; want position %q", tc.scheme, err, tc.position)
			}
		})
	}
}

func requireHandshake(t *testing.T) (*noiseConn, *noiseConn) {
	t.Helper()
	client, server, clientErr, serverErr := handshakePair(t, testKey(t), testKey(t), nil, nil, nil, nil)
	if clientErr != nil || serverErr != nil {
		t.Fatalf("handshake: client=%v server=%v", clientErr, serverErr)
	}
	return client, server
}

func exchange(t *testing.T, sender, receiver *noiseConn, data []byte) {
	t.Helper()
	written := make(chan error, 1)
	go func() {
		n, err := sender.Write(data)
		if err == nil && n != len(data) {
			err = fmt.Errorf("short write: %d of %d", n, len(data))
		}
		written <- err
	}()
	got := make([]byte, len(data))
	_, readErr := io.ReadFull(receiver, got)
	if err := <-written; err != nil {
		t.Fatalf("write %d bytes: %v", len(data), err)
	}
	if readErr != nil {
		t.Fatalf("read %d bytes: %v", len(data), readErr)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("payload mismatch for %d bytes", len(data))
	}
}

func TestNoiseHandshakeAndExchange(t *testing.T) {
	client, server := requireHandshake(t)
	for _, size := range []int{1, 16384, 65519} {
		data := bytes.Repeat([]byte("anoiss"), size/6+1)[:size]
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			exchange(t, client, server, data)
			exchange(t, server, client, data)
		})
	}
}

func TestNoiseHandshakeWrongKey(t *testing.T) {
	clientKey, serverKey, otherKey := testKey(t), testKey(t), testKey(t)
	client, _, clientErr, serverErr := handshakePair(t, clientKey, serverKey, serverKey.Public, nil, nil,
		map[string]bool{string(otherKey.Public): true})
	if clientErr != nil {
		t.Fatalf("XX handshake itself should succeed: %v", clientErr)
	}
	if serverErr == nil || !strings.Contains(serverErr.Error(), "rejected client with unknown public key") {
		t.Fatalf("expected whitelist rejection, got %v", serverErr)
	}
	var b [1]byte
	if _, err := client.Read(b[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("expected closed connection after rejection, got %v", err)
	}
}

func TestNoisePSK(t *testing.T) {
	clientKey, serverKey := testKey(t), testKey(t)
	psk := bytes.Repeat([]byte{0x51}, 32)
	t.Run("matching", func(t *testing.T) {
		client, server, clientErr, serverErr := handshakePair(t, clientKey, serverKey, serverKey.Public, psk, psk, nil)
		if clientErr != nil || serverErr != nil {
			t.Fatalf("matching PSK: client=%v server=%v", clientErr, serverErr)
		}
		exchange(t, client, server, []byte("psk works"))
	})
	t.Run("mismatching", func(t *testing.T) {
		wrong := bytes.Repeat([]byte{0x52}, 32)
		_, _, _, serverErr := handshakePair(t, clientKey, serverKey, serverKey.Public, psk, wrong, nil)
		if serverErr == nil {
			t.Fatal("server accepted mismatching PSK")
		}
	})
}

func TestNoiseShortFrame(t *testing.T) {
	client, server := requireHandshake(t)
	written := make(chan error, 1)
	go func() { written <- writeFrame(server.Conn, bytes.Repeat([]byte{0xff}, 8)) }()
	var b [1]byte
	_, err := client.Read(b[:])
	if err == nil || !strings.Contains(err.Error(), "frame too short: 8") {
		t.Fatalf("expected short-frame error with length 8, got %v", err)
	}
	client.Close()
	<-written
}

func TestNoiseEmptyFrame(t *testing.T) {
	client, server := requireHandshake(t)
	tag, err := server.sendCS.Encrypt(nil, nil, nil)
	if err != nil || len(tag) != noiseTagSize {
		t.Fatalf("encrypt empty payload: length=%d error=%v", len(tag), err)
	}
	written := make(chan error, 1)
	go func() { written <- writeFrame(server.Conn, tag) }()
	var b [1]byte
	n, err := client.Read(b[:])
	if n != 0 || err == nil || !strings.Contains(err.Error(), "empty frame") {
		t.Fatalf("expected empty-frame error, got (%d, %v)", n, err)
	}
	if err := <-written; err != nil {
		t.Fatalf("write empty frame: %v", err)
	}
}

func TestNoiseLargePayload(t *testing.T) {
	client, server := requireHandshake(t)
	exchange(t, client, server, bytes.Repeat([]byte("0123456789abcdef"), 1<<16))
}

func TestNoiseServerPublicKeyPin(t *testing.T) {
	clientKey, serverKey, wrongKey := testKey(t), testKey(t), testKey(t)
	for _, tc := range []struct {
		name string
		pin  []byte
		bad  bool
	}{
		{"correct", serverKey.Public, false},
		{"absent", nil, false},
		{"incorrect", wrongKey.Public, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server, clientErr, serverErr := handshakePair(t, clientKey, serverKey, tc.pin, nil, nil, nil)
			if tc.bad {
				if clientErr == nil || !strings.Contains(clientErr.Error(), "server public key mismatch") ||
					!strings.Contains(clientErr.Error(), base64.RawURLEncoding.EncodeToString(tc.pin)) ||
					!strings.Contains(clientErr.Error(), base64.RawURLEncoding.EncodeToString(serverKey.Public)) {
					t.Fatalf("expected pin mismatch with both keys, got %v", clientErr)
				}
				// The pin mismatch closes the connection mid-handshake (msg3),
				// so the server either fails the handshake outright or, if it
				// completed first, must read an error instead of session data.
				// A transport error and a clean io.EOF are both valid
				// manifestations of the closed connection.
				if serverErr != nil {
					return
				}
				var b [1]byte
				if _, err := server.Read(b[:]); err == nil {
					t.Fatal("expected error after pin mismatch, connection should be closed")
				}
				return
			}
			if serverErr != nil {
				t.Fatalf("server handshake: %v", serverErr)
			}
			if clientErr != nil {
				t.Fatalf("client handshake: %v", clientErr)
			}
			exchange(t, client, server, []byte("pinned exchange"))
		})
	}
}

func TestOutboundRequiresServerKey(t *testing.T) {
	key := testKey(t)
	validKey := base64.RawURLEncoding.EncodeToString(key.Public)
	clientKey := base64.RawURLEncoding.EncodeToString(testKey(t).Private)
	for _, tc := range []struct {
		name    string
		options option.AnoissOutboundOptions
		wantErr string
	}{
		{"missing_without_allow_any", option.AnoissOutboundOptions{
			ClientPrivateKey: clientKey, Password: "p",
		}, "server_public_key"},
		{"missing_with_allow_any", option.AnoissOutboundOptions{
			ClientPrivateKey: clientKey, Password: "p", AllowAnyServer: true,
		}, ""},
		{"valid_key", option.AnoissOutboundOptions{
			ClientPrivateKey: clientKey, Password: "p", ServerPublicKey: validKey,
		}, ""},
		{"valid_key_with_allow_any", option.AnoissOutboundOptions{
			ClientPrivateKey: clientKey, Password: "p", ServerPublicKey: validKey, AllowAnyServer: true,
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := log.NewNOPFactory().NewLogger("")
			_, err := NewOutbound(context.Background(), nil, logger, "test", tc.options)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// handshakePairAuth completes a Noise handshake with the given server-side
// authentication timeout. The returned deadline setter controls the client's
// underlying connection; the server's authentication deadline is managed by
// ServerHandshake and noiseConn.authRead.
func handshakePairAuth(t *testing.T, clientKey, serverKey noise.DHKey, authTimeout time.Duration) (*noiseConn, *noiseConn) {
	t.Helper()
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { clientRaw.Close(); serverRaw.Close() })
	serverDone := make(chan serverResult, 1)
	go func() {
		conn, peer, err := ServerHandshake(serverRaw, HandshakeOptions{
			StaticKey: serverKey, Timeout: 2 * time.Second, AuthTimeout: authTimeout,
		})
		serverDone <- serverResult{conn, peer, err}
	}()
	client, clientErr := ClientHandshake(clientRaw, HandshakeOptions{
		StaticKey: clientKey, Timeout: 2 * time.Second,
	})
	server := <-serverDone
	if clientErr != nil {
		t.Fatalf("client handshake: %v", clientErr)
	}
	if server.err != nil {
		t.Fatalf("server handshake: %v", server.err)
	}
	return client, server.conn
}

// writeAuthRequest writes a complete anytls authentication request:
// sha256(password) + uint16 big-endian padding length + padding bytes.
func writeAuthRequest(t *testing.T, conn net.Conn, password string, paddingLen int) {
	t.Helper()
	request := make([]byte, 34+paddingLen)
	sum := sha256.Sum256([]byte(password))
	copy(request, sum[:])
	binary.BigEndian.PutUint16(request[32:34], uint16(paddingLen))
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write auth request: %v", err)
	}
}

func TestServerAuthPhaseTimeout(t *testing.T) {
	client, server := handshakePairAuth(t, testKey(t), testKey(t), time.Second)
	defer client.Close()
	defer server.Close()

	type readResult struct {
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		var b [1]byte
		_, err := server.Read(b[:])
		done <- readResult{err}
	}()

	start := time.Now()
	select {
	case result := <-done:
		elapsed := time.Since(start)
		var ne net.Error
		if !errors.As(result.err, &ne) || !ne.Timeout() {
			t.Fatalf("expected timeout error, got %v", result.err)
		}
		if elapsed < 900*time.Millisecond || elapsed > 4*time.Second {
			t.Fatalf("timeout fired after %v, want ~1s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server read did not time out within 5s")
	}
}

func TestServerAuthPhaseTimeoutNotTriggered(t *testing.T) {
	client, server := handshakePairAuth(t, testKey(t), testKey(t), time.Second)
	defer client.Close()
	defer server.Close()

	// Complete the authentication phase well within the timeout, then keep
	// exchanging data past it; the deadline must have been cleared by authRead.
	// net.Pipe is synchronous, so the write must run in its own goroutine.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		writeAuthRequest(t, client, "password", 64)
	}()
	got := make([]byte, 34+64)
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("read auth request: %v", err)
	}
	<-writeDone

	time.Sleep(1100 * time.Millisecond)
	exchange(t, client, server, []byte("post-auth client to server"))
	exchange(t, server, client, []byte("post-auth server to client"))
}

func TestPaddingSchemeIndexZeroMax(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scheme  []string
		wantErr bool
	}{
		{"boundary_valid", []string{"stop=1", "0=65535-65535"}, false},
		{"exceeds_uint16", []string{"stop=1", "0=65536-65536"}, true},
		{"range_exceeds_uint16", []string{"stop=1", "0=100-70000"}, true},
		{"other_index_unrestricted", []string{"stop=1", "1=1-70000"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePaddingScheme(tc.scheme)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("expected valid scheme, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "65535") {
				t.Fatalf("expected error mentioning 65535, got %v", err)
			}
		})
	}
}
