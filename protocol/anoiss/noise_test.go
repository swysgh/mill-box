package anoiss

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
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
	t.Helper()
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { clientRaw.Close(); serverRaw.Close() })
	serverDone := make(chan serverResult, 1)
	go func() {
		conn, peer, err := ServerHandshake(serverRaw, serverKey, serverPSK, 2*time.Second)
		if err == nil && whitelist != nil && !(&Inbound{clientPubKeys: whitelist}).clientAllowed(peer) {
			conn.Close()
			err = fmt.Errorf("rejected client with unknown public key: %s", base64.RawURLEncoding.EncodeToString(peer))
		}
		serverDone <- serverResult{conn, peer, err}
	}()
	client, clientErr := ClientHandshake(clientRaw, clientKey, pin, clientPSK, 2*time.Second)
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
			if serverErr != nil {
				t.Fatalf("server handshake: %v", serverErr)
			}
			if tc.bad {
				if clientErr == nil || !strings.Contains(clientErr.Error(), "server public key mismatch") ||
					!strings.Contains(clientErr.Error(), base64.RawURLEncoding.EncodeToString(tc.pin)) ||
					!strings.Contains(clientErr.Error(), base64.RawURLEncoding.EncodeToString(serverKey.Public)) {
					t.Fatalf("expected pin mismatch with both keys, got %v", clientErr)
				}
				var b [1]byte
				if _, err := server.Read(b[:]); !errors.Is(err, io.EOF) {
					t.Fatalf("expected client to close connection, got %v", err)
				}
				return
			}
			if clientErr != nil {
				t.Fatalf("client handshake: %v", clientErr)
			}
			exchange(t, client, server, []byte("pinned exchange"))
		})
	}
}
