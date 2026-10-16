package uss

import (
	"sync"
	"time"
)

// fragmentPayload splits payload into at most fragmentCountMax chunks of at
// most maxChunk bytes. It returns the payload untouched when it already fits,
// so the caller can decide whether to emit the fragment header at all, and nil
// when the datagram can not be described by the fragment header at all, which
// the caller must treat as "drop this datagram".
func fragmentPayload(payload []byte, maxChunk int) [][]byte {
	if maxChunk < 1 {
		return nil
	}
	if len(payload) <= maxChunk {
		return [][]byte{payload}
	}
	chunkCount := (len(payload) + maxChunk - 1) / maxChunk
	if chunkCount > fragmentCountMax {
		// Unreachable with a validated mtu floor: 255 fragments of the smallest
		// allowed datagram already cover more than a UDP payload can hold.
		return nil
	}
	chunks := make([][]byte, 0, chunkCount)
	for offset := 0; offset < len(payload); offset += maxChunk {
		end := offset + maxChunk
		if end > len(payload) {
			end = len(payload)
		}
		chunks = append(chunks, payload[offset:end])
	}
	return chunks
}

type fragmentKey struct {
	sessionID  uint32
	fragmentID uint32
}

type reassemblyEntry struct {
	total    uint8
	received uint8
	length   int
	chunks   [][]byte
	deadline time.Time
}

// reassembler collects fragments of oversized datagrams. It is bounded in both
// entry count and buffered bytes, and every entry expires, so a peer can not
// turn lost fragments into unbounded memory.
type reassembler struct {
	access    sync.Mutex
	entries   map[fragmentKey]*reassemblyEntry
	maxEntry  int
	maxBytes  int
	usedBytes int
	timeout   time.Duration
	now       func() time.Time
}

func newReassembler(maxEntry int, maxBytes int, timeout time.Duration) *reassembler {
	if maxEntry < 1 {
		maxEntry = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	if timeout <= 0 {
		timeout = reassemblyTimeout
	}
	return &reassembler{
		entries:  make(map[fragmentKey]*reassemblyEntry),
		maxEntry: maxEntry,
		maxBytes: maxBytes,
		timeout:  timeout,
		now:      time.Now,
	}
}

// add records one fragment and returns the assembled payload once the last
// missing piece arrived.
func (r *reassembler) add(key fragmentKey, index uint8, total uint8, payload []byte) ([]byte, bool) {
	if total == 0 || index >= total {
		return nil, false
	}
	now := r.now()
	r.access.Lock()
	defer r.access.Unlock()
	r.expireLocked(now)
	entry, loaded := r.entries[key]
	if !loaded {
		if len(r.entries) >= r.maxEntry {
			return nil, false
		}
		entry = &reassemblyEntry{
			total:    total,
			chunks:   make([][]byte, total),
			deadline: now.Add(r.timeout),
		}
		r.entries[key] = entry
	}
	if entry.total != total {
		// The peer changed its mind about the fragment count mid-datagram.
		r.releaseLocked(key, entry)
		return nil, false
	}
	if entry.chunks[index] != nil {
		return nil, false
	}
	// The byte budget has to be enforced for every fragment, not only when the
	// entry is created: otherwise a peer could open an entry with a tiny first
	// fragment and then push the rest of a 255 fragment datagram through it.
	if r.usedBytes+len(payload) > r.maxBytes {
		r.releaseLocked(key, entry)
		return nil, false
	}
	chunk := make([]byte, len(payload))
	copy(chunk, payload)
	entry.chunks[index] = chunk
	entry.received++
	entry.length += len(chunk)
	r.usedBytes += len(chunk)
	if entry.received != entry.total {
		return nil, false
	}
	assembled := make([]byte, 0, entry.length)
	for _, piece := range entry.chunks {
		assembled = append(assembled, piece...)
	}
	r.releaseLocked(key, entry)
	return assembled, true
}

func (r *reassembler) expireLocked(now time.Time) {
	for key, entry := range r.entries {
		if now.After(entry.deadline) {
			r.releaseLocked(key, entry)
		}
	}
}

func (r *reassembler) releaseLocked(key fragmentKey, entry *reassemblyEntry) {
	for _, chunk := range entry.chunks {
		r.usedBytes -= len(chunk)
	}
	delete(r.entries, key)
}

// expire drops entries whose first fragment arrived too long ago. It is called
// periodically as well as on every add, so buffers of a datagram that was never
// completed do not outlive their timeout.
func (r *reassembler) expire() {
	r.access.Lock()
	defer r.access.Unlock()
	r.expireLocked(r.now())
}

// close drops every pending entry.
func (r *reassembler) close() {
	r.access.Lock()
	defer r.access.Unlock()
	r.entries = make(map[fragmentKey]*reassemblyEntry)
	r.usedBytes = 0
}
