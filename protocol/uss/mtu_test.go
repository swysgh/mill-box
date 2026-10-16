package uss

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
)

func testMTUConfig() mtuConfig {
	return mtuConfig{
		enabled:       true,
		base:          1280,
		max:           1500,
		step:          8,
		probeInterval: time.Hour,
		probeTimeout:  200 * time.Millisecond,
		probeAttempts: 1,
	}
}

// newEchoProber builds a prober whose probes are answered as soon as they fit
// the simulated path limit.
func newEchoProber(config mtuConfig, pathLimit int) *mtuProber {
	var prober *mtuProber
	prober = newMTUProber(config, logger.NOP(), func(probeID uint32, size int) error {
		if size <= pathLimit {
			prober.handleAck(probeID)
		}
		return nil
	}, nil)
	return prober
}

func TestMTUProberConverges(t *testing.T) {
	// 1400 is on the step grid, so the search must land on it exactly.
	prober := newEchoProber(testMTUConfig(), 1400)
	prober.search(context.Background())
	if got := prober.currentSize(); got != 1400 {
		t.Fatalf("expected the prober to settle at 1400, got %d", got)
	}
}

func TestMTUProberOffGridLimit(t *testing.T) {
	// A path limit between two steps must round down to the lower step.
	prober := newEchoProber(testMTUConfig(), 1394)
	prober.search(context.Background())
	if got := prober.currentSize(); got != 1392 {
		t.Fatalf("expected the prober to settle at 1392, got %d", got)
	}
}

func TestMTUProberKeepsBaseWhenNothingFits(t *testing.T) {
	// Probing must never go below the configured floor: 1280 is the smallest
	// datagram every path is required to carry.
	prober := newEchoProber(testMTUConfig(), 1000)
	prober.search(context.Background())
	if got := prober.currentSize(); got != 1280 {
		t.Fatalf("expected the prober to stay at the base, got %d", got)
	}
}

func TestMTUProberReachesMax(t *testing.T) {
	prober := newEchoProber(testMTUConfig(), 9000)
	prober.search(context.Background())
	if got := prober.currentSize(); got != 1500 {
		t.Fatalf("expected the prober to reach the maximum, got %d", got)
	}
}

func TestMTUProberShrinksAfterPathChange(t *testing.T) {
	config := testMTUConfig()
	pathLimit := 1450
	var prober *mtuProber
	prober = newMTUProber(config, logger.NOP(), func(probeID uint32, size int) error {
		if size <= pathLimit {
			prober.handleAck(probeID)
		}
		return nil
	}, nil)
	prober.search(context.Background())
	if got := prober.currentSize(); got != 1448 {
		t.Fatalf("expected 1448 before the path change, got %d", got)
	}
	// A route change lowers the usable size; a second round must notice instead
	// of trusting the stale value.
	pathLimit = 1300
	prober.search(context.Background())
	if got := prober.currentSize(); got != 1296 {
		t.Fatalf("expected the prober to shrink to 1296, got %d", got)
	}
}

func TestMTUProberObserveReceived(t *testing.T) {
	config := testMTUConfig()
	config.enabled = false
	prober := newMTUProber(config, logger.NOP(), func(probeID uint32, size int) error {
		t.Fatal("a passive prober must not send anything")
		return nil
	}, nil)
	if got := prober.currentSize(); got != 1280 {
		t.Fatalf("expected the base as the starting point, got %d", got)
	}
	prober.observeReceived(1350)
	if got := prober.currentSize(); got != 1350 {
		t.Fatalf("expected 1350 after observation, got %d", got)
	}
	// Smaller datagrams must not lower the estimate: they only prove what
	// already worked.
	prober.observeReceived(1300)
	if got := prober.currentSize(); got != 1350 {
		t.Fatalf("estimate shrank on a smaller datagram: %d", got)
	}
	prober.observeReceived(9000)
	if got := prober.currentSize(); got != 1500 {
		t.Fatalf("observation exceeded the configured maximum: %d", got)
	}
}

func TestMTUConfigValidation(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(config *mtuConfig)
		ok     bool
	}{
		{"default", func(config *mtuConfig) {}, true},
		{"base below minimum", func(config *mtuConfig) { config.base = 100 }, false},
		{"base above max", func(config *mtuConfig) { config.base = 1400; config.max = 1300 }, false},
		{"max too large", func(config *mtuConfig) { config.max = 4000 }, false},
		{"step larger than range", func(config *mtuConfig) { config.step = 500 }, false},
		{"zero timeout", func(config *mtuConfig) { config.probeTimeout = 0 }, false},
		{"zero attempts", func(config *mtuConfig) { config.probeAttempts = 0 }, false},
		{"fixed size", func(config *mtuConfig) { config.max = config.base; config.step = 8 }, true},
	} {
		config := testMTUConfig()
		testCase.mutate(&config)
		err := config.validate()
		if testCase.ok && err != nil {
			t.Fatalf("%s: unexpected error: %v", testCase.name, err)
		}
		if !testCase.ok && err == nil {
			t.Fatalf("%s: expected an error", testCase.name)
		}
	}
}

func TestMTUProberKeepsEstimateOnTransientError(t *testing.T) {
	// A send error that is not "message too long" says nothing about the path,
	// so the estimate must not be lowered.
	prober := newMTUProber(testMTUConfig(), logger.NOP(), func(probeID uint32, size int) error {
		return errors.New("socket busy")
	}, nil)
	prober.observeReceived(1440)
	prober.search(context.Background())
	if got := prober.currentSize(); got != 1440 {
		t.Fatalf("a transient error changed the estimate to %d", got)
	}
}

func TestMTUProberIgnoresStaleAck(t *testing.T) {
	// A late acknowledgement of an abandoned probe must not consume an attempt
	// or make a perfectly good size look like a black hole.
	config := testMTUConfig()
	config.probeAttempts = 1
	var prober *mtuProber
	prober = newMTUProber(config, logger.NOP(), func(probeID uint32, size int) error {
		if size <= 1400 {
			prober.handleAck(probeID - 1)
			prober.handleAck(probeID)
		}
		return nil
	}, nil)
	prober.search(context.Background())
	if got := prober.currentSize(); got != 1400 {
		t.Fatalf("a stale acknowledgement changed the result to %d", got)
	}
}

func TestKCPMTUBudgetFloor(t *testing.T) {
	// A budget that can not hold a KCP header must never reach the ARQ layer:
	// kcp-go derives its segment size from it, and a negative value turns into
	// a huge segment because its flush loop sizes things unsigned.
	for _, datagramSize := range []int{-4096, 0, 1, 16, 32, minKCPMTU, minKCPMTU + cleartextSize + aeadTagSize - 1} {
		if got := kcpMTUBudget(datagramSize); got < minKCPMTU {
			t.Fatalf("kcpMTUBudget(%d) = %d, want at least %d", datagramSize, got, minKCPMTU)
		}
	}
	want := 1500 - cleartextSize - aeadTagSize - 1
	if got := kcpMTUBudget(1500); got != want {
		t.Fatalf("kcpMTUBudget(1500) = %d, want %d", got, want)
	}
}

func TestMTUProberNeverProbesAboveDatagramBound(t *testing.T) {
	// A large step must not push the search ceiling above what a peer is able
	// to receive: the probe size is a whole datagram.
	config := mtuConfig{
		enabled:       true,
		base:          576,
		max:           maxDatagramSize - cleartextSize - aeadTagSize,
		step:          maxDatagramSize - cleartextSize - aeadTagSize - 576,
		probeInterval: time.Hour,
		probeTimeout:  time.Second,
		probeAttempts: 1,
	}
	if err := config.validate(); err != nil {
		t.Fatalf("config must be valid: %v", err)
	}
	prober := newEchoProber(config, config.max)
	for i := 0; i < 64; i++ {
		size := prober.nextProbeSize()
		if size <= 0 {
			return
		}
		if size > maxDatagramSize {
			t.Fatalf("probe size %d exceeds the datagram bound %d", size, maxDatagramSize)
		}
		prober.record(size, true)
	}
}
