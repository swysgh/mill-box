package uss

import (
	"time"

	"github.com/swysgh/mill-box/option"
)

const (
	defaultPingInterval     = 25 * time.Second
	defaultUDPTimeout       = 5 * time.Minute
	defaultSessionTimeout   = 10 * time.Minute
	defaultHandshakeTimeout = 10 * time.Second
	defaultMaxSessions      = 256
	defaultMaxUDPFlows      = 512
	defaultMaxKCPChannels   = 32
	defaultReplayWindowSize = 1024

	defaultMTUBase          = 1280
	defaultMTUMax           = 1500
	defaultMTUStep          = 8
	defaultMTUProbeInterval = 10 * time.Minute
	defaultMTUProbeTimeout  = time.Second
	defaultMTUProbeAttempts = 2
)

// buildKCPConfig resolves the ARQ tunables, keeping kcp-go's defaults for
// everything the configuration file does not mention.
func buildKCPConfig(options *option.UssKCPOptions) kcpConfig {
	config := defaultKCPConfig()
	if options == nil {
		return config
	}
	if options.NoDelay != nil {
		config.noDelay = *options.NoDelay
	}
	if options.Interval != nil {
		config.interval = *options.Interval
	}
	if options.Resend != nil {
		config.resend = *options.Resend
	}
	if options.NoCongestionControl != nil {
		config.noCongestionControl = *options.NoCongestionControl
	}
	if options.WindowSize != nil {
		config.windowSize = *options.WindowSize
	}
	if options.MaxStreamBuffer != nil {
		config.maxStreamBuffer = *options.MaxStreamBuffer
	}
	if options.MaxReceiveBuffer != nil {
		config.maxReceiveBuffer = *options.MaxReceiveBuffer
	}
	if options.KeepAliveInterval != 0 {
		config.keepAliveInterval = time.Duration(options.KeepAliveInterval)
	}
	if options.KeepAliveTimeout != 0 {
		config.keepAliveTimeout = time.Duration(options.KeepAliveTimeout)
	}
	return config
}

// buildMTUConfig resolves the path mtu discovery tunables. Active probing is
// only useful on the client: the server learns the same path passively from the
// datagrams that reach it, so its default is off.
func buildMTUConfig(options *option.UssMTUOptions, activeByDefault bool) mtuConfig {
	config := mtuConfig{
		enabled:       activeByDefault,
		base:          defaultMTUBase,
		max:           defaultMTUMax,
		step:          defaultMTUStep,
		probeInterval: defaultMTUProbeInterval,
		probeTimeout:  defaultMTUProbeTimeout,
		probeAttempts: defaultMTUProbeAttempts,
	}
	if options == nil {
		return config
	}
	if options.Enabled != nil {
		config.enabled = *options.Enabled
	}
	if options.Base != nil {
		config.base = *options.Base
	}
	if options.Max != nil {
		config.max = *options.Max
	}
	if options.Step != nil {
		config.step = *options.Step
	}
	if options.ProbeInterval != 0 {
		config.probeInterval = time.Duration(options.ProbeInterval)
	}
	if options.ProbeTimeout != 0 {
		config.probeTimeout = time.Duration(options.ProbeTimeout)
	}
	if options.ProbeAttempts != nil {
		config.probeAttempts = *options.ProbeAttempts
	}
	return config
}

func durationOr(value time.Duration, fallback time.Duration) time.Duration {
	if value != 0 {
		return value
	}
	return fallback
}

func intOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}
