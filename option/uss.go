package option

import (
	"github.com/sagernet/sing/common/json/badoption"
)

type UssInboundOptions struct {
	ListenOptions
	PreSharedKey     string             `json:"pre_shared_key"`
	UDPTimeout       badoption.Duration `json:"udp_timeout,omitempty"`
	SessionTimeout   badoption.Duration `json:"session_timeout,omitempty"`
	HandshakeTimeout badoption.Duration `json:"handshake_timeout,omitempty"`
	MaxSessions      *int               `json:"max_sessions,omitempty"`
	MaxUDPFlows      *int               `json:"max_udp_flows,omitempty"`
	MaxKCPChannels   *int               `json:"max_kcp_channels,omitempty"`
	ReplayWindowSize *int               `json:"replay_window_size,omitempty"`
	DisableTCP       bool               `json:"disable_tcp,omitempty"`
	DisableUDP       bool               `json:"disable_udp,omitempty"`
	KCP              *UssKCPOptions     `json:"kcp,omitempty"`
	MTU              *UssMTUOptions     `json:"mtu,omitempty"`
}

type UssOutboundOptions struct {
	DialerOptions
	ServerOptions
	PreSharedKey     string             `json:"pre_shared_key"`
	TCPMode          string             `json:"tcp_mode,omitempty"`
	UDPMode          string             `json:"udp_mode,omitempty"`
	UDPTimeout       badoption.Duration `json:"udp_timeout,omitempty"`
	SessionTimeout   badoption.Duration `json:"session_timeout,omitempty"`
	MaxUDPFlows      *int               `json:"max_udp_flows,omitempty"`
	ReplayWindowSize *int               `json:"replay_window_size,omitempty"`
	PingInterval     badoption.Duration `json:"ping_interval,omitempty"`
	KCP              *UssKCPOptions     `json:"kcp,omitempty"`
	MTU              *UssMTUOptions     `json:"mtu,omitempty"`
}

type UssKCPOptions struct {
	NoDelay             *int               `json:"nodelay,omitempty"`
	Interval            *int               `json:"interval,omitempty"`
	Resend              *int               `json:"resend,omitempty"`
	NoCongestionControl *int               `json:"nc,omitempty"`
	WindowSize          *int               `json:"window_size,omitempty"`
	MaxStreamBuffer     *int               `json:"max_stream_buffer,omitempty"`
	MaxReceiveBuffer    *int               `json:"max_receive_buffer,omitempty"`
	KeepAliveInterval   badoption.Duration `json:"keep_alive_interval,omitempty"`
	KeepAliveTimeout    badoption.Duration `json:"keep_alive_timeout,omitempty"`
}

type UssMTUOptions struct {
	Enabled       *bool              `json:"enabled,omitempty"`
	Base          *int               `json:"base,omitempty"`
	Max           *int               `json:"max,omitempty"`
	Step          *int               `json:"step,omitempty"`
	ProbeInterval badoption.Duration `json:"probe_interval,omitempty"`
	ProbeTimeout  badoption.Duration `json:"probe_timeout,omitempty"`
	ProbeAttempts *int               `json:"probe_attempts,omitempty"`
}
