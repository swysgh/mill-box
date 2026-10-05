package anoiss

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
)

const (
	prologue                = "anoiss/1"
	maxPlaintext            = 16384
	noiseTagSize            = 16
	maxFramePayload         = noise.MaxMsgLen - noiseTagSize // 65519
	headerSize              = 2
	defaultHandshakeTimeout = 5 * time.Second
)

// noiseConn wraps a net.Conn with Noise protocol encryption.
// It implements net.Conn and provides thread-safe reads and writes.
//
// NOTE: sing-anytls padding scheme is designed for TLS record plaintext lengths;
// anoiss has no TLS layer, and frames have 2-byte length prefix + 16-byte AEAD tag,
// so padding target lengths are offset by 18 bytes. Kept as-is for now.
type noiseConn struct {
	net.Conn
	sendCS *noise.CipherState
	recvCS *noise.CipherState

	writeMu sync.Mutex
	readMu  sync.Mutex

	readBuf []byte // decrypted plaintext buffer
	readOff int    // current read offset into readBuf
}

func newNoiseConn(inner net.Conn, sendCS, recvCS *noise.CipherState) *noiseConn {
	return &noiseConn{
		Conn:   inner,
		sendCS: sendCS,
		recvCS: recvCS,
	}
}

func (c *noiseConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	// Return buffered plaintext first
	if c.readOff < len(c.readBuf) {
		n := copy(b, c.readBuf[c.readOff:])
		c.readOff += n
		if c.readOff >= len(c.readBuf) {
			c.readBuf = nil
			c.readOff = 0
		}
		return n, nil
	}

	// Read frame header
	var hdr [headerSize]byte
	if _, err := io.ReadFull(c.Conn, hdr[:]); err != nil {
		return 0, err
	}
	frameLen := int(binary.BigEndian.Uint16(hdr[:]))
	if frameLen < noiseTagSize {
		return 0, fmt.Errorf("anoiss: frame too short: %d < %d", frameLen, noiseTagSize)
	}

	// Read ciphertext
	ciphertext := make([]byte, frameLen)
	if _, err := io.ReadFull(c.Conn, ciphertext); err != nil {
		return 0, err
	}

	// Decrypt
	plaintext, err := c.recvCS.Decrypt(nil, nil, ciphertext)
	if err != nil {
		return 0, fmt.Errorf("anoiss: decrypt error: %w", err)
	}
	if len(plaintext) == 0 {
		return 0, fmt.Errorf("anoiss: empty frame")
	}

	n := copy(b, plaintext)
	if n < len(plaintext) {
		c.readBuf = plaintext
		c.readOff = n
	}
	return n, nil
}

func (c *noiseConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	total := 0
	for len(b) > 0 {
		chunk := b
		if len(chunk) > maxPlaintext {
			chunk = chunk[:maxPlaintext]
		}

		ciphertext, err := c.sendCS.Encrypt(nil, nil, chunk)
		if err != nil {
			return total, fmt.Errorf("anoiss: encrypt error: %w", err)
		}

		var hdr [headerSize]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(ciphertext)))

		if _, err := c.Conn.Write(hdr[:]); err != nil {
			return total, err
		}
		if _, err := c.Conn.Write(ciphertext); err != nil {
			return total, err
		}

		total += len(chunk)
		b = b[len(chunk):]
	}
	return total, nil
}

// Transparent forwarding for deadline support (required by sing-anytls)
func (c *noiseConn) SetDeadline(t time.Time) error      { return c.Conn.SetDeadline(t) }
func (c *noiseConn) SetReadDeadline(t time.Time) error  { return c.Conn.SetReadDeadline(t) }
func (c *noiseConn) SetWriteDeadline(t time.Time) error { return c.Conn.SetWriteDeadline(t) }

// Forward CloseWrite/CloseRead if the underlying conn supports them
// (sing-box helpers do type assertions on these).

type closeWriter interface{ CloseWrite() error }
type closeReader interface{ CloseRead() error }

func (c *noiseConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *noiseConn) CloseRead() error {
	if cr, ok := c.Conn.(closeReader); ok {
		return cr.CloseRead()
	}
	return nil
}

// newCipherSuite returns the Noise cipher suite for anoiss.
func newCipherSuite() noise.CipherSuite {
	return noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)
}

// writeFrame writes a length-prefixed frame to the connection.
func writeFrame(conn net.Conn, data []byte) error {
	var hdr [headerSize]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(data)))
	if _, err := conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err := conn.Write(data)
	return err
}

// readFrame reads a length-prefixed frame from the connection.
func readFrame(conn net.Conn) ([]byte, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	frameLen := int(binary.BigEndian.Uint16(hdr[:]))
	if frameLen > noise.MaxMsgLen {
		return nil, fmt.Errorf("anoiss: handshake frame too large: %d", frameLen)
	}
	data := make([]byte, frameLen)
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}
	return data, nil
}

// ClientHandshake performs a Noise XX handshake as initiator.
// Returns a noiseConn wrapping the provided connection.
func ClientHandshake(conn net.Conn, staticKey noise.DHKey, expectedPeerStatic []byte, psk []byte, timeout time.Duration) (*noiseConn, error) {
	if timeout <= 0 {
		timeout = defaultHandshakeTimeout
	}
	deadline := time.Now().Add(timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	defer conn.SetDeadline(time.Time{})

	cs := newCipherSuite()
	cfg := noise.Config{
		CipherSuite:   cs,
		Random:        rand.Reader,
		Pattern:       noise.HandshakeXX,
		Initiator:     true,
		Prologue:      []byte(prologue),
		StaticKeypair: staticKey,
	}
	if len(psk) == 32 {
		cfg.PresharedKey = psk
		cfg.PresharedKeyPlacement = 3
	}

	hs, err := noise.NewHandshakeState(cfg)
	if err != nil {
		return nil, fmt.Errorf("anoiss: client handshake init: %w", err)
	}

	// Message 1: client → server (e)
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("anoiss: client write msg1: %w", err)
	}
	if err := writeFrame(conn, msg1); err != nil {
		return nil, fmt.Errorf("anoiss: client send msg1: %w", err)
	}

	// Message 2: server → client (e, ee, s, es)
	msg2, err := readFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("anoiss: client read msg2: %w", err)
	}
	_, _, _, err = hs.ReadMessage(nil, msg2)
	if err != nil {
		return nil, fmt.Errorf("anoiss: client process msg2: %w", err)
	}

	// Message 3: client → server (s, se) — completes handshake
	msg3, cs1, cs2, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("anoiss: client write msg3: %w", err)
	}
	if err := writeFrame(conn, msg3); err != nil {
		return nil, fmt.Errorf("anoiss: client send msg3: %w", err)
	}
	if len(expectedPeerStatic) > 0 {
		actual := hs.PeerStatic()
		if !bytes.Equal(actual, expectedPeerStatic) {
			conn.Close()
			return nil, fmt.Errorf("anoiss: server public key mismatch: expected %s, got %s",
				base64.RawURLEncoding.EncodeToString(expectedPeerStatic),
				base64.RawURLEncoding.EncodeToString(actual))
		}
	}

	// §2.4: Initiator: send = cs1, recv = cs2
	return newNoiseConn(conn, cs1, cs2), nil
}

// ServerHandshake performs a Noise XX handshake as responder.
// Returns a noiseConn and the peer's static public key.
func ServerHandshake(conn net.Conn, staticKey noise.DHKey, psk []byte, timeout time.Duration) (*noiseConn, []byte, error) {
	if timeout <= 0 {
		timeout = defaultHandshakeTimeout
	}
	deadline := time.Now().Add(timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}
	defer conn.SetDeadline(time.Time{})

	cs := newCipherSuite()
	cfg := noise.Config{
		CipherSuite:   cs,
		Random:        rand.Reader,
		Pattern:       noise.HandshakeXX,
		Initiator:     false,
		Prologue:      []byte(prologue),
		StaticKeypair: staticKey,
	}
	if len(psk) == 32 {
		cfg.PresharedKey = psk
		cfg.PresharedKeyPlacement = 3
	}

	hs, err := noise.NewHandshakeState(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("anoiss: server handshake init: %w", err)
	}

	// Message 1: client → server
	msg1, err := readFrame(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("anoiss: server read msg1: %w", err)
	}
	_, _, _, err = hs.ReadMessage(nil, msg1)
	if err != nil {
		return nil, nil, fmt.Errorf("anoiss: server process msg1: %w", err)
	}

	// Message 2: server → client (e, ee, s, es)
	msg2, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("anoiss: server write msg2: %w", err)
	}
	if err := writeFrame(conn, msg2); err != nil {
		return nil, nil, fmt.Errorf("anoiss: server send msg2: %w", err)
	}

	// Message 3: client → server (s, se) — completes handshake
	msg3, err := readFrame(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("anoiss: server read msg3: %w", err)
	}
	_, cs1, cs2, err := hs.ReadMessage(nil, msg3)
	if err != nil {
		return nil, nil, fmt.Errorf("anoiss: server process msg3: %w", err)
	}

	peerStatic := hs.PeerStatic()

	// §2.4: Responder: send = cs2, recv = cs1
	return newNoiseConn(conn, cs2, cs1), peerStatic, nil
}

// decodeKey decodes a base64-encoded 32-byte key.
// Tries RawURLEncoding first, then StdEncoding.
func decodeKey(fieldName, encoded string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("anoiss: %s: invalid base64 encoding", fieldName)
		}
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("anoiss: %s: expected 32 bytes, got %d", fieldName, len(decoded))
	}
	return decoded, nil
}
