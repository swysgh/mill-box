package uss

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	keySize = 32

	infoClientToServer = "uss/v1/client-to-server"
	infoServerToClient = "uss/v1/server-to-client"
)

// sessionKeys holds the pair of AEAD instances used for one client instance.
//
// The pre-shared key is shared by every client of a server, so it can not be
// used directly: two clients starting their counter at zero would reuse the
// same (key, nonce) pair, which destroys both confidentiality and integrity.
// Both directions are therefore derived per client instance id, which also
// makes the instance id impossible to forge.
type sessionKeys struct {
	send cipher.AEAD
	recv cipher.AEAD
}

func deriveSessionKeys(psk []byte, instanceID [instanceIDSize]byte, isClient bool) (*sessionKeys, error) {
	clientToServer, err := deriveKey(psk, instanceID[:], infoClientToServer)
	if err != nil {
		return nil, E.Cause(err, "derive client-to-server key")
	}
	serverToClient, err := deriveKey(psk, instanceID[:], infoServerToClient)
	if err != nil {
		return nil, E.Cause(err, "derive server-to-client key")
	}
	sendAEAD, err := chacha20poly1305.New(clientToServer)
	if err != nil {
		return nil, E.Cause(err, "create client-to-server aead")
	}
	recvAEAD, err := chacha20poly1305.New(serverToClient)
	if err != nil {
		return nil, E.Cause(err, "create server-to-client aead")
	}
	if isClient {
		return &sessionKeys{send: sendAEAD, recv: recvAEAD}, nil
	}
	return &sessionKeys{send: recvAEAD, recv: sendAEAD}, nil
}

// deriveKey turns the pre shared key into the key of one client instance and
// one direction. The pre shared key is already a uniformly random 32 byte
// secret, so the extract step of HKDF would add nothing (RFC 5869 section 3.3):
// a single keyed hash over a domain separated input is enough, and it halves
// the work the server does before it can authenticate anything.
func deriveKey(psk []byte, instanceID []byte, info string) ([]byte, error) {
	mac := hmac.New(sha256.New, psk)
	mac.Write([]byte(info))
	mac.Write([]byte{0})
	mac.Write(instanceID)
	return mac.Sum(nil), nil
}

// seal appends an encrypted datagram for the given instance id to dst.
//
// dst must have room for cleartextSize+len(plaintext)+aeadTagSize bytes.
func (k *sessionKeys) seal(dst []byte, instanceID [instanceIDSize]byte, counter uint64, plaintext []byte) []byte {
	offset := len(dst)
	dst = append(dst, make([]byte, cleartextSize+len(plaintext)+aeadTagSize)...)
	putInstanceID(dst[offset:], instanceID)
	putCounter(dst[offset+instanceIDSize:], counter)
	var nonce [chacha20poly1305.NonceSize]byte
	putCounter(nonce[:], counter)
	// The cleartext prefix is authenticated: it selects the session, so it must
	// not be mutable in flight.
	k.send.Seal(dst[offset:offset+cleartextSize], nonce[:], plaintext, dst[offset:offset+cleartextSize])
	return dst
}

// open decrypts a datagram in place and returns the plaintext slice.
func (k *sessionKeys) open(datagram []byte) ([]byte, error) {
	if len(datagram) < cleartextSize+aeadTagSize {
		return nil, errShortPacket
	}
	counter := getCounter(datagram[instanceIDSize:])
	var nonce [chacha20poly1305.NonceSize]byte
	putCounter(nonce[:], counter)
	// Decrypt in place: passing the zero length slice that starts where the
	// ciphertext starts reuses the same storage, which is the documented way to
	// ask for an in place open.
	plaintext, err := k.recv.Open(datagram[cleartextSize:cleartextSize], nonce[:], datagram[cleartextSize:], datagram[:cleartextSize])
	if err != nil {
		return nil, errDecrypt
	}
	return plaintext, nil
}

// replayWindow is the anti-replay sliding window described in RFC 6479: the
// newest accepted counter sits at bit 0 and older ones are shifted up by one
// bit per received packet.
type replayWindow struct {
	access   sync.Mutex
	bitCount uint64
	bits     []uint64
	newest   uint64
	started  bool
}

func newReplayWindow(bitCount int) *replayWindow {
	if bitCount < 64 {
		bitCount = 64
	}
	// Round up to a whole number of words so the shift loop stays simple.
	bitCount = (bitCount + 63) / 64 * 64
	return &replayWindow{
		bitCount: uint64(bitCount),
		bits:     make([]uint64, bitCount/64),
	}
}

// accept reports whether counter is fresh, and records it.
func (w *replayWindow) accept(counter uint64) bool {
	w.access.Lock()
	defer w.access.Unlock()
	if !w.started {
		w.started = true
		w.newest = counter
		w.setBit(0)
		return true
	}
	if counter > w.newest {
		w.shift(counter - w.newest)
		w.newest = counter
		w.setBit(0)
		return true
	}
	distance := w.newest - counter
	if distance >= w.bitCount {
		return false
	}
	if w.bit(distance) {
		return false
	}
	w.setBit(distance)
	return true
}

func (w *replayWindow) shift(distance uint64) {
	if distance >= w.bitCount {
		clear(w.bits)
		return
	}
	words := int(distance / 64)
	remainder := distance % 64
	if words > 0 {
		for i := len(w.bits) - 1; i >= words; i-- {
			w.bits[i] = w.bits[i-words]
		}
		clear(w.bits[:words])
	}
	if remainder > 0 {
		for i := len(w.bits) - 1; i > 0; i-- {
			w.bits[i] = w.bits[i]<<remainder | w.bits[i-1]>>(64-remainder)
		}
		w.bits[0] <<= remainder
	}
}

func (w *replayWindow) bit(index uint64) bool {
	return w.bits[index/64]&(1<<(index%64)) != 0
}

func (w *replayWindow) setBit(index uint64) {
	w.bits[index/64] |= 1 << (index % 64)
}

// keyCache memoises the per-instance keys on the server so that HKDF runs once
// per client instance instead of once per datagram.
type keyCache struct {
	access sync.Mutex
	keys   map[[instanceIDSize]byte]*sessionKeys
	limit  int
}

func newKeyCache(limit int) *keyCache {
	if limit < 1 {
		limit = 1
	}
	return &keyCache{
		keys:  make(map[[instanceIDSize]byte]*sessionKeys),
		limit: limit,
	}
}

func (c *keyCache) get(psk []byte, instanceID [instanceIDSize]byte) (*sessionKeys, bool) {
	c.access.Lock()
	keys, loaded := c.keys[instanceID]
	c.access.Unlock()
	if loaded {
		return keys, true
	}
	keys, err := deriveSessionKeys(psk, instanceID, false)
	if err != nil {
		return nil, false
	}
	c.access.Lock()
	// A full cache drops everything rather than evicting one entry: the next
	// datagram of each live instance re-derives its key, which is cheap, and
	// this keeps a hostile peer from pinning one victim's key out of the cache.
	if len(c.keys) >= c.limit {
		c.keys = make(map[[instanceIDSize]byte]*sessionKeys)
	}
	c.keys[instanceID] = keys
	c.access.Unlock()
	return keys, true
}

func (c *keyCache) remove(instanceID [instanceIDSize]byte) {
	c.access.Lock()
	delete(c.keys, instanceID)
	c.access.Unlock()
}

// decodeKey parses a 32 byte pre-shared key from the usual textual encodings.
func decodeKey(name string, value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, E.New(name, " is required")
	}
	decode := func(encoding *base64.Encoding) []byte {
		decoded, err := encoding.DecodeString(value)
		if err != nil {
			return nil
		}
		return decoded
	}
	// A string can be valid in more than one encoding (a 64 character hex key is
	// also valid base64), so every candidate is tried and only the one with the
	// right length wins.
	var lengthError error
	check := func(decoded []byte) ([]byte, bool) {
		if len(decoded) == keySize {
			return decoded, true
		}
		if lengthError == nil {
			lengthError = E.New(name, ": decoded key must be ", keySize, " bytes, got ", len(decoded))
		}
		return nil, false
	}
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		decoded := decode(encoding)
		if decoded == nil {
			continue
		}
		if key, ok := check(decoded); ok {
			return key, nil
		}
	}
	if decoded, err := hex.DecodeString(value); err == nil {
		if key, ok := check(decoded); ok {
			return key, nil
		}
	}
	if lengthError != nil {
		return nil, lengthError
	}
	return nil, E.New(name, ": invalid key, expected base64 or hex encoded ", keySize, " bytes")
}
