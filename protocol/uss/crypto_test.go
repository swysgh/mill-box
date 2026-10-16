package uss

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	psk := bytes.Repeat([]byte{0x42}, keySize)
	instance := randomInstance(t)
	clientKeys, err := deriveSessionKeys(psk, instance, true)
	if err != nil {
		t.Fatal(err)
	}
	serverKeys, err := deriveSessionKeys(psk, instance, false)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("the quick brown fox")
	datagram := clientKeys.seal(nil, instance, 7, plaintext)
	if len(datagram) != cleartextSize+len(plaintext)+aeadTagSize {
		t.Fatalf("unexpected datagram size: got %d", len(datagram))
	}
	decrypted, err := serverKeys.open(datagram)
	if err != nil {
		t.Fatal("server could not decrypt client datagram: ", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("payload mismatch: got %q", decrypted)
	}
	// The server answers on the other key, which the client must be able to read.
	reply := serverKeys.seal(nil, instance, 0, []byte("pong"))
	decryptedReply, err := clientKeys.open(reply)
	if err != nil {
		t.Fatal("client could not decrypt server datagram: ", err)
	}
	if !bytes.Equal(decryptedReply, []byte("pong")) {
		t.Fatalf("reply mismatch: got %q", decryptedReply)
	}
}

func TestSealOpenRejectsTampering(t *testing.T) {
	psk := bytes.Repeat([]byte{0x11}, keySize)
	instance := randomInstance(t)
	clientKeys, _ := deriveSessionKeys(psk, instance, true)
	serverKeys, _ := deriveSessionKeys(psk, instance, false)
	original := clientKeys.seal(nil, instance, 1, []byte("payload"))

	for _, testCase := range []struct {
		name   string
		mutate func(datagram []byte)
	}{
		{"instance id", func(d []byte) { d[0] ^= 0xff }},
		{"counter", func(d []byte) { d[instanceIDSize] ^= 0x01 }},
		{"ciphertext", func(d []byte) { d[cleartextSize] ^= 0x80 }},
		{"tag", func(d []byte) { d[len(d)-1] ^= 0x80 }},
	} {
		datagram := append([]byte(nil), original...)
		testCase.mutate(datagram)
		if _, err := serverKeys.open(datagram); err == nil {
			t.Fatalf("tampered %s was accepted", testCase.name)
		}
	}
}

func TestKeysAreBoundToInstance(t *testing.T) {
	psk := bytes.Repeat([]byte{0x33}, keySize)
	first := randomInstance(t)
	second := randomInstance(t)
	firstKeys, _ := deriveSessionKeys(psk, first, true)
	secondKeys, _ := deriveSessionKeys(psk, second, false)
	// Two client instances must never share a key stream: reusing a (key, nonce)
	// pair across instances would leak plaintext and break the tag.
	datagram := firstKeys.seal(nil, first, 0, []byte("hello"))
	if _, err := secondKeys.open(datagram); err == nil {
		t.Fatal("datagram from one instance decrypted with another instance's key")
	}
}

func TestReplayWindowAcceptsInOrder(t *testing.T) {
	window := newReplayWindow(1024)
	for counter := uint64(0); counter < 4096; counter++ {
		if !window.accept(counter) {
			t.Fatalf("fresh counter %d was rejected", counter)
		}
	}
	if window.accept(4095) {
		t.Fatal("replayed counter was accepted")
	}
	if window.accept(0) {
		t.Fatal("counter far outside the window was accepted")
	}
}

func TestReplayWindowAllowsReordering(t *testing.T) {
	window := newReplayWindow(1024)
	if !window.accept(100) {
		t.Fatal("first counter rejected")
	}
	// Anything inside the window may still arrive late, exactly once.
	for _, counter := range []uint64{101, 103, 102, 99, 98, 50} {
		if !window.accept(counter) {
			t.Fatalf("counter %d rejected inside the window", counter)
		}
		if window.accept(counter) {
			t.Fatalf("counter %d accepted twice", counter)
		}
	}
	if window.accept(100) {
		t.Fatal("already seen counter accepted again")
	}
}

func TestReplayWindowDropsOldCounters(t *testing.T) {
	window := newReplayWindow(64)
	if !window.accept(0) {
		t.Fatal("first counter rejected")
	}
	if !window.accept(1000) {
		t.Fatal("forward jump rejected")
	}
	// A 64 bit window covers distances 0..63, so 1000-63 = 937 is the oldest
	// counter that may still arrive.
	if window.accept(936) {
		t.Fatal("counter at the window edge was accepted")
	}
	if !window.accept(937) {
		t.Fatal("oldest counter inside the window was rejected")
	}
}

func TestReplayWindowZeroCounter(t *testing.T) {
	// A fresh window must accept counter zero, which is the first packet of a
	// session and would be mistaken for "unset" by a naive implementation.
	window := newReplayWindow(64)
	if !window.accept(0) {
		t.Fatal("counter zero rejected")
	}
	if window.accept(0) {
		t.Fatal("counter zero accepted twice")
	}
}

func TestDecodeKey(t *testing.T) {
	raw := bytes.Repeat([]byte{0x5a}, keySize)
	for _, testCase := range []struct {
		name  string
		value string
		ok    bool
	}{
		{"raw url base64", base64.RawURLEncoding.EncodeToString(raw), true},
		{"padded url base64", base64.URLEncoding.EncodeToString(raw), true},
		{"standard base64", base64.StdEncoding.EncodeToString(raw), true},
		{"hex", hex.EncodeToString(raw), true},
		{"empty", "", false},
		{"too short", base64.RawURLEncoding.EncodeToString(raw[:16]), false},
		{"garbage", "not-a-key", false},
	} {
		decoded, err := decodeKey("pre_shared_key", testCase.value)
		if testCase.ok {
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", testCase.name, err)
			}
			if !bytes.Equal(decoded, raw) {
				t.Fatalf("%s: decoded key mismatch", testCase.name)
			}
		} else if err == nil {
			t.Fatalf("%s: expected an error", testCase.name)
		}
	}
}

func TestKeyCache(t *testing.T) {
	psk := bytes.Repeat([]byte{0x77}, keySize)
	cache := newKeyCache(2)
	first := randomInstance(t)
	second := randomInstance(t)
	third := randomInstance(t)
	keys, loaded := cache.get(psk, first)
	if !loaded {
		t.Fatal("first lookup failed")
	}
	again, loaded := cache.get(psk, first)
	if !loaded || again != keys {
		t.Fatal("cache did not return the memoised keys")
	}
	cache.get(psk, second)
	// The third entry overflows the cache, which drops everything rather than
	// evicting a single victim.
	cache.get(psk, third)
	if _, loaded = cache.get(psk, third); !loaded {
		t.Fatal("lookup after cache flush failed")
	}
	// The cache holds server side keys, so the probe has to be produced with
	// the matching client side pair.
	reference, _ := deriveSessionKeys(psk, third, true)
	cached, _ := cache.get(psk, third)
	probe := reference.seal(nil, third, 5, []byte("x"))
	if _, err := cached.open(probe); err != nil {
		t.Fatal("cached key does not match a freshly derived one: ", err)
	}
}

func randomInstance(t *testing.T) [instanceIDSize]byte {
	t.Helper()
	var instance [instanceIDSize]byte
	_, err := rand.Read(instance[:])
	if err != nil {
		t.Fatal(err)
	}
	return instance
}
