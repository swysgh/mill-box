package uss

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/swysgh/mill-box/adapter"
	"github.com/swysgh/mill-box/adapter/inbound"
	"github.com/swysgh/mill-box/common/listener"
	"github.com/swysgh/mill-box/common/uot"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.UssInboundOptions](registry, C.TypeUss, NewInbound)
}

type Inbound struct {
	inbound.Adapter
	ctx      context.Context
	router   adapter.ConnectionRouterEx
	logger   logger.ContextLogger
	listener *listener.Listener
	config   serverConfig
	// The listener starts reading before the transport exists, so the handoff
	// has to be atomic: a datagram may arrive at any moment.
	transport atomic.Pointer[serverTransport]
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.UssInboundOptions) (adapter.Inbound, error) {
	psk, err := decodeKey("pre_shared_key", options.PreSharedKey)
	if err != nil {
		return nil, err
	}
	in := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeUss, tag),
		ctx:     ctx,
		// The UoT router turns a stream opened to the udp-over-tcp magic
		// address back into a packet connection, which is what udp_mode: kcp
		// relies on. It is transparent for every other destination.
		router: uot.NewRouter(router, logger),
		logger: logger,
		config: serverConfig{
			psk:              psk,
			tcpEnabled:       !options.DisableTCP,
			udpEnabled:       !options.DisableUDP,
			kcp:              buildKCPConfig(options.KCP),
			mtu:              buildMTUConfig(options.MTU, false),
			sessionTimeout:   durationOr(time.Duration(options.SessionTimeout), defaultSessionTimeout),
			handshakeTimeout: durationOr(time.Duration(options.HandshakeTimeout), defaultHandshakeTimeout),
			udpTimeout:       durationOr(time.Duration(options.UDPTimeout), defaultUDPTimeout),
			maxSessions:      intOr(options.MaxSessions, defaultMaxSessions),
			maxUDPFlows:      intOr(options.MaxUDPFlows, defaultMaxUDPFlows),
			maxKCPChannels:   intOr(options.MaxKCPChannels, defaultMaxKCPChannels),
			replayWindowSize: intOr(options.ReplayWindowSize, defaultReplayWindowSize),
		},
	}
	if err := in.config.validate(); err != nil {
		return nil, E.Cause(err, "uss inbound")
	}
	in.listener = listener.New(listener.Options{
		Context: ctx,
		Logger:  logger,
		Network: []string{N.NetworkUDP},
		Listen:  options.ListenOptions,
		// The handler takes ownership of the buffer, so the listener must hand
		// out a fresh one per datagram instead of reusing a single buffer.
		PacketHandler:            in,
		ThreadUnsafePacketWriter: true,
	})
	return in, nil
}

func (h *Inbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	err := h.listener.Start()
	if err != nil {
		return err
	}
	scope.Add(h.listener.Close)
	transport, err := newServerTransport(
		h.ctx,
		h.logger,
		h.config,
		h.router,
		h.Tag(),
		h.listener.PacketWriter(),
		h.listener.UDPAddr().UDPAddr(),
	)
	if err != nil {
		return err
	}
	h.transport.Store(transport)
	scope.Add(func() error {
		transport.close()
		return nil
	})
	h.logger.Info("uss inbound: listen=", h.listener.UDPAddr(),
		" tcp=", h.config.tcpEnabled,
		" udp=", h.config.udpEnabled,
		" session_timeout=", h.config.sessionTimeout,
		" handshake_timeout=", h.config.handshakeTimeout,
		" udp_timeout=", h.config.udpTimeout,
		" max_sessions=", h.config.maxSessions,
		" max_udp_flows=", h.config.maxUDPFlows,
		" max_kcp_channels=", h.config.maxKCPChannels,
		" replay_window_size=", h.config.replayWindowSize,
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
	if h.config.mtu.enabled {
		h.logger.Warn("uss: mtu.enabled is set on the inbound, so the server will probe clients too; the client already discovers the path and the server learns it passively, so this is only useful for diagnostics")
	}
	if h.config.udpEnabled && common.PtrValueOrDefault(h.listener.ListenOptions().UDPFragment) {
		h.logger.Warn("uss: udp_fragment is enabled on this inbound, so the kernel fragments oversized datagrams instead of dropping them and path mtu discovery will report the maximum size as usable; disable udp_fragment to get a real result")
	}
	return nil
}

// NewPacket is called once per datagram that reached the listener. The listener
// transfers ownership of the buffer, so it is released here.
func (h *Inbound) NewPacket(buffer *buf.Buffer, source M.Socksaddr) {
	defer buffer.Release()
	transport := h.transport.Load()
	if transport == nil {
		return
	}
	transport.handlePacket(buffer.Bytes(), source)
}

func (h *Inbound) NewError(ctx context.Context, err error) {
	if E.IsClosedOrCanceled(err) {
		h.logger.DebugContext(ctx, "connection closed: ", err)
		return
	}
	h.logger.ErrorContext(ctx, err)
}
