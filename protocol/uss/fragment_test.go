package uss

import (
	"bytes"
	"testing"
	"time"
)

func TestFragmentPayload(t *testing.T) {
	payload := bytes.Repeat([]byte{0xab}, 100)
	chunks := fragmentPayload(payload, 40)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	for index, chunk := range chunks {
		if len(chunk) > 40 {
			t.Fatalf("chunk %d is larger than the limit: %d", index, len(chunk))
		}
	}
	rebuilt := bytes.Join(chunks, nil)
	if !bytes.Equal(rebuilt, payload) {
		t.Fatal("chunks do not rebuild the payload")
	}
	// A payload that already fits must stay in one piece.
	if chunks = fragmentPayload(payload, 100); len(chunks) != 1 {
		t.Fatalf("payload that fits was split into %d chunks", len(chunks))
	}
	// A datagram that can not be described by the fragment header must be
	// reported as undeliverable instead of being sent as one oversized packet.
	huge := bytes.Repeat([]byte{0xcd}, fragmentCountMax*2)
	if chunks = fragmentPayload(huge, 1); chunks != nil {
		t.Fatalf("oversized fragment count produced %d chunks", len(chunks))
	}
	if chunks = fragmentPayload([]byte("x"), 0); chunks != nil {
		t.Fatal("a zero chunk budget produced chunks")
	}
	// An empty datagram is legal and must survive as a single chunk.
	if chunks = fragmentPayload(nil, 100); len(chunks) != 1 {
		t.Fatal("empty datagram was dropped")
	}
}

func TestReassemblerOutOfOrder(t *testing.T) {
	reassembler := newReassembler(16, 1<<20, time.Minute)
	key := fragmentKey{sessionID: 7, fragmentID: 1}
	if _, complete := reassembler.add(key, 2, 3, []byte("ccc")); complete {
		t.Fatal("incomplete datagram reported as complete")
	}
	if _, complete := reassembler.add(key, 0, 3, []byte("aaa")); complete {
		t.Fatal("incomplete datagram reported as complete")
	}
	assembled, complete := reassembler.add(key, 1, 3, []byte("bbb"))
	if !complete {
		t.Fatal("complete datagram not reported")
	}
	if string(assembled) != "aaabbbccc" {
		t.Fatalf("wrong order: %q", assembled)
	}
}

func TestReassemblerIgnoresDuplicates(t *testing.T) {
	reassembler := newReassembler(16, 1<<20, time.Minute)
	key := fragmentKey{sessionID: 1, fragmentID: 1}
	reassembler.add(key, 0, 2, []byte("aa"))
	if _, complete := reassembler.add(key, 0, 2, []byte("aa")); complete {
		t.Fatal("duplicate fragment completed the datagram")
	}
	assembled, complete := reassembler.add(key, 1, 2, []byte("bb"))
	if !complete || string(assembled) != "aabb" {
		t.Fatalf("unexpected result: %q complete=%v", assembled, complete)
	}
}

func TestReassemblerRejectsChangedTotal(t *testing.T) {
	reassembler := newReassembler(16, 1<<20, time.Minute)
	key := fragmentKey{sessionID: 1, fragmentID: 1}
	reassembler.add(key, 0, 3, []byte("aa"))
	// A fragment count that changes mid datagram can not be reconciled, so the
	// buffer is dropped instead of being mixed together.
	if _, complete := reassembler.add(key, 1, 2, []byte("bb")); complete {
		t.Fatal("fragment count change was accepted")
	}
	if len(reassembler.entries) != 0 {
		t.Fatal("inconsistent datagram was kept")
	}
}

func TestReassemblerRejectsInvalidIndex(t *testing.T) {
	reassembler := newReassembler(16, 1<<20, time.Minute)
	key := fragmentKey{sessionID: 1, fragmentID: 1}
	if _, complete := reassembler.add(key, 3, 3, []byte("x")); complete {
		t.Fatal("out of range index accepted")
	}
	if _, complete := reassembler.add(key, 0, 0, []byte("x")); complete {
		t.Fatal("zero fragment count accepted")
	}
}

func TestReassemblerLimits(t *testing.T) {
	reassembler := newReassembler(1, 8, time.Minute)
	first := fragmentKey{sessionID: 1, fragmentID: 1}
	second := fragmentKey{sessionID: 2, fragmentID: 1}
	if _, complete := reassembler.add(first, 0, 2, []byte("1234")); complete {
		t.Fatal("unexpected completion")
	}
	// The entry limit is reached, so a second datagram is dropped instead of
	// growing the table.
	if _, complete := reassembler.add(second, 0, 2, []byte("1234")); complete {
		t.Fatal("unexpected completion")
	}
	if len(reassembler.entries) != 1 {
		t.Fatalf("expected 1 pending entry, got %d", len(reassembler.entries))
	}
	// The byte limit applies to a fresh datagram as well.
	reassembler.close()
	if _, complete := reassembler.add(first, 0, 2, bytes.Repeat([]byte{1}, 16)); complete {
		t.Fatal("unexpected completion")
	}
	if len(reassembler.entries) != 0 {
		t.Fatalf("datagram larger than the byte limit was buffered")
	}
}

func TestReassemblerExpiry(t *testing.T) {
	reassembler := newReassembler(16, 1<<20, time.Second)
	now := time.Now()
	reassembler.now = func() time.Time { return now }
	key := fragmentKey{sessionID: 1, fragmentID: 1}
	reassembler.add(key, 0, 2, []byte("aa"))
	if len(reassembler.entries) != 1 {
		t.Fatal("fragment was not buffered")
	}
	now = now.Add(2 * time.Second)
	if _, complete := reassembler.add(key, 1, 2, []byte("bb")); complete {
		t.Fatal("expired fragments were still used")
	}
	// The stale first fragment was dropped, so only the freshly buffered one is
	// accounted for.
	if reassembler.usedBytes != 2 {
		t.Fatalf("expected only the new fragment to be buffered, got %d bytes", reassembler.usedBytes)
	}
}

func TestReassemblerByteBudgetOnEveryFragment(t *testing.T) {
	// A peer must not be able to open a tiny entry and then push an unbounded
	// amount of data through it.
	reassembler := newReassembler(16, 32, time.Minute)
	key := fragmentKey{sessionID: 1, fragmentID: 1}
	if _, complete := reassembler.add(key, 0, 4, []byte("aa")); complete {
		t.Fatal("unexpected completion")
	}
	if reassembler.usedBytes != 2 {
		t.Fatalf("expected 2 buffered bytes, got %d", reassembler.usedBytes)
	}
	if _, complete := reassembler.add(key, 1, 4, bytes.Repeat([]byte{1}, 64)); complete {
		t.Fatal("unexpected completion")
	}
	// The entry can never complete inside the budget, so it is released whole.
	if reassembler.usedBytes != 0 {
		t.Fatalf("fragment over the byte budget was buffered: %d bytes", reassembler.usedBytes)
	}
	if len(reassembler.entries) != 0 {
		t.Fatal("entry over the byte budget was kept")
	}
}
