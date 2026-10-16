package uss

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/swysgh/mill-box/adapter"
	"github.com/swysgh/mill-box/adapter/outbound"
	"github.com/swysgh/mill-box/common/dialer"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"

	"github.com/sagernet/smux"
)

const (
	TCPModeKCP  = "kcp"
	TCPModeNone = "none"

	UDPModeRaw = "raw"
	UDPModeKCP = "kcp"
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.UssOutboundOptions](registry, C.TypeUss, NewOutbound)
}

var (
	_ adapter.Outbound                = (*Outbound)(nil)
	_ adapter.InterfaceUpdateListener = (*Outbound)(nil)
	_ adapter.OutboundWithMultiplex   = (*Outbound)(nil)
)

type Outbound struct {
	outbound.Adapter
	ctx         context.Context
	logger      log.ContextLogger
	server      M.Socksaddr
	dialer      N.Dialer
	config      clientConfig
	tcpMode     string
	udpMode     string
	transport   atomic.Pointer[clientTransport]
	uotClient   *uot.Client
	udpFragment bool
}

// clientTransportRef returns the shared transport once the outbound has been
// started.
func (h *Outbound) clientTransportRef() *clientTransport {
	return h.transport.Load()
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.UssOutboundOptions) (adapter.Outbound, error) {
	psk, err := decodeKey("pre_shared_key", options.PreSharedKey)
	if err != nil {
		return nil, err
	}
	tcpMode := options.TCPMode
	if tcpMode == "" {
		tcpMode = TCPModeKCP
	}
	switch tcpMode {
	case TCPModeKCP, TCPModeNone:
	default:
		return nil, E.New("unknown tcp_mode: ", tcpMode, " (expected ", TCPModeKCP, " or ", TCPModeNone, ")")
	}
	udpMode := options.UDPMode
	if udpMode == "" {
		udpMode = UDPModeRaw
	}
	switch udpMode {
	case UDPModeRaw, UDPModeKCP:
	default:
		return nil, E.New("unknown udp_mode: ", udpMode, " (expected ", UDPModeRaw, " or ", UDPModeKCP, ")")
	}
	if tcpMode == TCPModeNone && udpMode == UDPModeKCP {
		return nil, E.New("udp_mode ", UDPModeKCP, " needs tcp_mode ", TCPModeKCP, ": it carries datagrams over the reliable channel")
	}
	server := options.ServerOptions.Build()
	if !server.IsValid() {
		return nil, E.New("missing server address")
	}
	network := []string{N.NetworkUDP}
	if tcpMode == TCPModeKCP {
		network = append(network, N.NetworkTCP)
	}
	out := &Outbound{
		Adapter:     outbound.NewAdapterWithDialerOptions(C.TypeUss, tag, network, options.DialerOptions),
		ctx:         ctx,
		logger:      logger,
		server:      server,
		tcpMode:     tcpMode,
		udpMode:     udpMode,
		udpFragment: common.PtrValueOrDefault(options.UDPFragment),
		config: clientConfig{
			server:           server,
			psk:              psk,
			tcpEnabled:       tcpMode == TCPModeKCP,
			kcp:              buildKCPConfig(options.KCP),
			mtu:              buildMTUConfig(options.MTU, true),
			pingInterval:     durationOr(time.Duration(options.PingInterval), defaultPingInterval),
			udpTimeout:       durationOr(time.Duration(options.UDPTimeout), defaultUDPTimeout),
			maxUDPFlows:      intOr(options.MaxUDPFlows, defaultMaxUDPFlows),
			replayWindowSize: intOr(options.ReplayWindowSize, defaultReplayWindowSize),
		},
	}
	// Validate here rather than in Start so that "mill-box check" reports a bad
	// configuration instead of the service failing at startup.
	if err = out.config.validate(); err != nil {
		return nil, E.Cause(err, "uss outbound")
	}
	outboundDialer, err := dialer.NewWithOptions(dialer.Options{
		Context:        ctx,
		Options:        options.DialerOptions,
		RemoteIsDomain: options.ServerIsDomain(),
	})
	if err != nil {
		return nil, err
	}
	out.dialer = outboundDialer
	if udpMode == UDPModeKCP {
		out.uotClient = &uot.Client{
			Dialer:  out,
			Version: uot.Version,
		}
	}
	return out, nil
}

func (h *Outbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateInitialize {
		return nil
	}
	transport, err := newClientTransport(h.ctx, h.logger, h.config, func(ctx context.Context) (net.Conn, error) {
		return h.dialer.DialContext(ctx, N.NetworkUDP, h.server)
	})
	if err != nil {
		return err
	}
	h.transport.Store(transport)
	scope.Add(func() error {
		transport.close()
		return nil
	})
	h.logger.Info("uss outbound: server=", h.server,
		" tcp_mode=", h.tcpMode,
		" udp_mode=", h.udpMode,
		" udp_timeout=", h.config.udpTimeout,
		" max_udp_flows=", h.config.maxUDPFlows,
		" ping_interval=", h.config.pingInterval,
		" mtu_probe=", h.config.mtu.enabled,
		" mtu_base=", h.config.mtu.base,
		" mtu_max=", h.config.mtu.max,
		" mtu_step=", h.config.mtu.step,
		" mtu_probe_interval=", h.config.mtu.probeInterval,
		" mtu_probe_timeout=", h.config.mtu.probeTimeout,
		" mtu_probe_attempts=", h.config.mtu.probeAttempts,
		" kcp_nodelay=", h.config.kcp.noDelay,
		" kcp_interval=", h.config.kcp.interval,
		" kcp_resend=", h.config.kcp.resend,
		" kcp_nc=", h.config.kcp.noCongestionControl,
		" kcp_window_size=", h.config.kcp.windowSize,
		" kcp_max_stream_buffer=", h.config.kcp.maxStreamBuffer,
		" kcp_max_receive_buffer=", h.config.kcp.maxReceiveBuffer,
		" kcp_keep_alive_interval=", h.config.kcp.keepAliveInterval,
		" kcp_keep_alive_timeout=", h.config.kcp.keepAliveTimeout,
	)
	if h.config.mtu.enabled && h.udpFragment {
		h.logger.Warn("uss: udp_fragment is enabled on this outbound, so the kernel fragments oversized datagrams instead of dropping them and path mtu discovery will report the maximum size as usable; disable udp_fragment to get a real result")
	}
	return nil
}

func (h *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	ctx, metadata := adapter.ExtendContext(ctx)
	metadata.Outbound = h.Tag()
	metadata.Destination = destination
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		if h.tcpMode != TCPModeKCP {
			return nil, E.New("uss: tcp is disabled by tcp_mode=", TCPModeNone)
		}
		h.logger.InfoContext(ctx, "outbound connection to ", destination)
		return h.dialTCP(ctx, destination)
	case N.NetworkUDP:
		if h.udpMode == UDPModeKCP {
			h.logger.InfoContext(ctx, "outbound UoT packet connection to ", destination)
			return h.uotClient.DialContext(ctx, network, destination)
		}
		h.logger.InfoContext(ctx, "outbound packet connection to ", destination)
		packetConn, err := h.ListenPacket(ctx, destination)
		if err != nil {
			return nil, err
		}
		return bufio.NewBindPacketConn(packetConn, destination.UDPAddr()), nil
	}
	return nil, E.Extend(N.ErrUnknownNetwork, network)
}

func (h *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ctx, metadata := adapter.ExtendContext(ctx)
	metadata.Outbound = h.Tag()
	metadata.Destination = destination
	if h.udpMode == UDPModeKCP {
		h.logger.InfoContext(ctx, "outbound UoT packet connection to ", destination)
		return h.uotClient.ListenPacket(ctx, destination)
	}
	transport := h.transport.Load()
	if transport == nil {
		return nil, os.ErrClosed
	}
	h.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	return newClientUDPSession(ctx, transport, h.logger), nil
}

// dialTCP opens one stream on the shared reliable channel. Short lived
// connections therefore cost a stream instead of a handshake.
func (h *Outbound) dialTCP(ctx context.Context, destination M.Socksaddr) (result net.Conn, err error) {
	if h.transport.Load() == nil {
		return nil, os.ErrClosed
	}
	stream, err := h.openStream(ctx)
	if err != nil {
		return nil, err
	}
	// A cancelled dial must not leave the stream half written.
	if ctx.Done() != nil {
		stopContext := context.AfterFunc(ctx, func() {
			stream.Close()
		})
		defer func() {
			if !stopContext() {
				stream.Close()
				result = nil
				err = ctx.Err()
			}
		}()
	}
	err = M.SocksaddrSerializer.WriteAddrPort(stream, destination)
	if err != nil {
		stream.Close()
		return nil, E.Cause(err, "write destination")
	}
	return stream, nil
}

func (h *Outbound) openStream(ctx context.Context) (*smux.Stream, error) {
	transport := h.transport.Load()
	if transport == nil {
		return nil, os.ErrClosed
	}
	session, err := transport.kcp()
	if err != nil {
		return nil, err
	}
	stream, err := session.openStream()
	if err == nil {
		return stream, nil
	}
	// The channel we reused is dead (server restart, network change). Build a
	// new one and try once more rather than failing the user's connection.
	h.logger.Debug("uss: reliable channel unusable, reconnecting: ", err)
	transport.resetKCP(session)
	session, err = transport.kcp()
	if err != nil {
		return nil, err
	}
	stream, err = session.openStream()
	if err != nil {
		return nil, E.Cause(err, "open stream")
	}
	return stream, nil
}

func (h *Outbound) MultiplexEnabled() bool {
	return h.tcpMode == TCPModeKCP
}

// InterfaceUpdated drops the reliable channel when the network changes so the
// next connection starts on the new path instead of on a dead one.
func (h *Outbound) InterfaceUpdated(ctx context.Context) {
	transport := h.transport.Load()
	if transport == nil {
		return
	}
	transport.resetKCPIfPresent()
}
