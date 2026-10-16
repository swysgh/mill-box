package uss

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// Wire layout of a single datagram:
//
//	[ instanceID (8) ][ counter (8) ][ AEAD ciphertext (payload + 16 tag) ]
//
// The 16 byte cleartext prefix is authenticated as AEAD associated data, so a
// forged instance id or counter fails decryption instead of being routed to
// somebody else's session.
const (
	instanceIDSize = 8
	counterSize    = 8
	cleartextSize  = instanceIDSize + counterSize
	aeadTagSize    = 16

	// maxDatagramSize bounds every buffer we allocate for a single datagram.
	// It is deliberately above the biggest payload we ever produce (mtu max +
	// our own overhead) so that a peer can never make us allocate more.
	maxDatagramSize = 2048

	// readBufferSize is the buffer the transport socket is read into. It is the
	// maximum a UDP datagram can be, which buys two things: a datagram larger
	// than maxDatagramSize stays readable (so it can be rejected with a warning
	// naming its size instead of vanishing into a failed read), and EMSGSIZE on
	// this socket can only ever mean the path's ICMP "fragmentation needed" —
	// never a datagram that did not fit the buffer. Processing still enforces
	// maxDatagramSize.
	readBufferSize = 65535
)

// Packet types, carried as the first plaintext byte. The explicit byte type
// keeps them from being silently compared against a wider integer.
const (
	packetTypeProbe    byte = 0x01
	packetTypeProbeAck byte = 0x02
	packetTypeKCP      byte = 0x03
	packetTypeUDP      byte = 0x04
	packetTypeUDPReply byte = 0x05
	packetTypePing     byte = 0x06
	packetTypePong     byte = 0x07
)

// Fixed body header of a raw datagram packet (packetTypeUDP / packetTypeUDPReply):
//
//	[ sessionID (4) ][ fragmentID (4) ][ fragmentIndex (1) ][ fragmentTotal (1) ]
//
// The fragment id is deliberately wide: it is a counter per flow, and it must
// not come back around while an incomplete datagram may still be buffered.
const rawHeaderSize = 10

// probe body: [ probeID (4) ]
//
// The id is 32 bits wide on purpose: it is a counter, and a late acknowledgement
// must never be mistaken for the answer to a probe that is running now.
const probeHeaderSize = 4

var (
	errShortPacket      = errors.New("uss: packet too short")
	errInvalidPacket    = errors.New("uss: invalid packet")
	errReplay           = errors.New("uss: replayed packet")
	errDecrypt          = errors.New("uss: decrypt failed")
	errFragmentTooLarge = errors.New("uss: fragment larger than path mtu")
	errReassemblyLimit  = errors.New("uss: too many pending fragments")
)

// Tunables that must be identical on both ends of a session, or are otherwise
// part of the protocol rather than of a single deployment.
const (
	// fragmentCountMax is the largest number of slices a single datagram may be
	// split into (the count lives in one byte).
	fragmentCountMax = 255

	// minKCPMTU is the floor handed to the ARQ layer. A segment budget below
	// it can not even carry a KCP header.
	minKCPMTU = 64

	// minRawChunkSize is the smallest fragment worth emitting. Below it the
	// datagram is dropped instead of being shredded into hundreds of packets.
	minRawChunkSize = 64

	// kcpHeaderSize is the fixed size of a KCP segment header; the first four
	// bytes are the conversation id, which the server needs before it can hand
	// the segment to the right session.
	kcpHeaderSize = 24

	// reassemblyTimeout bounds how long the first fragment of a datagram waits
	// for its siblings before the buffer is dropped.
	reassemblyTimeout = 10 * time.Second
)

func putInstanceID(dst []byte, instanceID [instanceIDSize]byte) {
	copy(dst[:instanceIDSize], instanceID[:])
}

func getInstanceID(src []byte) [instanceIDSize]byte {
	var instanceID [instanceIDSize]byte
	copy(instanceID[:], src[:instanceIDSize])
	return instanceID
}

func putCounter(dst []byte, counter uint64) {
	binary.BigEndian.PutUint64(dst, counter)
}

func getCounter(src []byte) uint64 {
	return binary.BigEndian.Uint64(src)
}

func readUint16(src []byte) uint16 {
	return binary.BigEndian.Uint16(src)
}

func readUint32(src []byte) uint32 {
	return binary.BigEndian.Uint32(src)
}

func putUint16(dst []byte, value uint16) {
	binary.BigEndian.PutUint16(dst, value)
}

func putUint32(dst []byte, value uint32) {
	binary.BigEndian.PutUint32(dst, value)
}

// socksaddrBox holds an address that can be read from any goroutine. A plain
// atomic.Value would force a type assertion on every read because Socksaddr is
// a struct.
type socksaddrBox struct {
	access sync.Mutex
	value  M.Socksaddr
}

func (b *socksaddrBox) Load() M.Socksaddr {
	b.access.Lock()
	defer b.access.Unlock()
	return b.value
}

func (b *socksaddrBox) Store(addr M.Socksaddr) {
	b.access.Lock()
	b.value = addr
	b.access.Unlock()
}

// copyPayload detaches a datagram payload from the buffer it was read into.
func copyPayload(payload []byte) []byte {
	copied := make([]byte, len(payload))
	copy(copied, payload)
	return copied
}

// writeRawHeader fills the fixed part of a raw datagram body.
func writeRawHeader(dst []byte, sessionID uint32, fragmentID uint32, fragmentIndex uint8, fragmentTotal uint8) {
	putUint32(dst, sessionID)
	putUint32(dst[4:], fragmentID)
	dst[8] = fragmentIndex
	dst[9] = fragmentTotal
}
