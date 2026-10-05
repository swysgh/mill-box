package anoiss

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/flynn/noise"
	"github.com/sagernet/sing-anytls"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.AnoissOutboundOptions](registry, C.TypeAnoiss, NewOutbound)
}

var (
	_ adapter.OutboundWithMultiplex   = (*Outbound)(nil)
	_ adapter.InterfaceUpdateListener = (*Outbound)(nil)
	_ adapter.IdleConnectionKeeper    = (*Outbound)(nil)
)

type Outbound struct {
	outbound.Adapter
	rawDialer          N.Dialer
	server             M.Socksaddr
	clientOptions      anytls.ClientOptions
	client             *anytls.Client
	uotClient          *uot.Client
	logger             log.ContextLogger
	staticKey          noise.DHKey
	expectedPeerStatic []byte
	psk                []byte
	hsTimeout          time.Duration
	hsPadding          int
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AnoissOutboundOptions) (adapter.Outbound, error) {
	out := &Outbound{
		Adapter: outbound.NewAdapterWithDialerOptions(C.TypeAnoiss, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.DialerOptions),
		server:  options.ServerOptions.Build(),
		logger:  logger,
	}

	// Parse client private key
	if options.ClientPrivateKey == "" {
		return nil, E.New("anoiss: client_private_key is required")
	}
	privBytes, err := decodeKey("client_private_key", options.ClientPrivateKey)
	if err != nil {
		return nil, err
	}
	pub, err := pubFromPriv(privBytes)
	if err != nil {
		return nil, err
	}
	out.staticKey = noise.DHKey{Private: privBytes, Public: pub}

	// Parse server public key (optional, for pinning)
	if options.ServerPublicKey != "" {
		out.expectedPeerStatic, err = decodeKey("server_public_key", options.ServerPublicKey)
		if err != nil {
			return nil, err
		}
	}

	// Parse PSK
	if options.PreSharedKey != "" {
		out.psk, err = decodeKey("pre_shared_key", options.PreSharedKey)
		if err != nil {
			return nil, err
		}
	}

	out.hsTimeout = options.HandshakeTimeout.Build()
	out.hsPadding = configuredHandshakePadding(options.HandshakePadding)
	if err := validateHandshakePadding(out.hsPadding); err != nil {
		return nil, err
	}

	outboundDialer, err := dialer.NewWithOptions(dialer.Options{
		Context:        ctx,
		Options:        options.DialerOptions,
		RemoteIsDomain: options.ServerIsDomain(),
	})
	if err != nil {
		return nil, err
	}
	out.rawDialer = outboundDialer

	out.clientOptions = anytls.ClientOptions{
		Password:                 options.Password,
		ClientMetadata:           options.ClientMetadata,
		DialOut:                  out.dialOut,
		IdleSessionCheckInterval: options.IdleSessionCheckInterval.Build(),
		IdleSessionTimeout:       options.IdleSessionTimeout.Build(),
		MinIdleSession:           options.MinIdleSession,
		Logger:                   logger,
	}
	return out, nil
}

func (h *Outbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateInitialize {
		return nil
	}

	// Log startup info (§6.3)
	fingerprint := sha256.Sum256(h.staticKey.Public)
	h.logger.Info("anoiss outbound starting",
		" server=", h.server,
		" client_pubkey_fingerprint=", hex.EncodeToString(fingerprint[:8]),
		" psk=", len(h.psk) > 0,
		" handshake_timeout=", h.hsTimeout.String(),
		" handshake_padding=", fmt.Sprint(h.hsPadding),
	)

	client, err := anytls.NewClient(h.clientOptions)
	if err != nil {
		return err
	}
	h.client = client
	scope.Add(client.Close)
	h.uotClient = &uot.Client{
		Dialer:  anoissDialer(client.DialContext),
		Version: uot.Version,
	}
	return nil
}

type anoissDialer func(ctx context.Context, destination M.Socksaddr) (net.Conn, error)

func (d anoissDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return d(ctx, destination)
}

func (d anoissDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

func (h *Outbound) dialOut(ctx context.Context) (net.Conn, error) {
	conn, err := h.rawDialer.DialContext(adapter.ContextForMultiplexSession(ctx), N.NetworkTCP, h.server)
	if err != nil {
		return nil, err
	}
	noiseConn, err := ClientHandshake(conn, HandshakeOptions{
		StaticKey: h.staticKey, ExpectedPeer: h.expectedPeerStatic, PreSharedKey: h.psk,
		Timeout: h.hsTimeout, PaddingSize: h.hsPadding,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return noiseConn, nil
}

func (h *Outbound) MultiplexEnabled() bool {
	return true
}

func (h *Outbound) InterfaceUpdated(ctx context.Context) {
	h.client.Reset()
}

func (h *Outbound) SetKeepIdleConnections(keep bool) {
	h.client.SetKeepIdleConnections(keep)
}

func (h *Outbound) CloseIdleConnections() {
	h.client.CloseIdleConnections()
}

func (h *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	ctx, metadata := adapter.ExtendContext(ctx)
	metadata.Outbound = h.Tag()
	metadata.Destination = destination
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		h.logger.InfoContext(ctx, "outbound connection to ", destination)
		return h.client.DialContext(ctx, destination)
	case N.NetworkUDP:
		h.logger.InfoContext(ctx, "outbound UoT packet connection to ", destination)
		return h.uotClient.DialContext(ctx, network, destination)
	}
	return nil, os.ErrInvalid
}

func (h *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ctx, metadata := adapter.ExtendContext(ctx)
	metadata.Outbound = h.Tag()
	metadata.Destination = destination
	h.logger.InfoContext(ctx, "outbound UoT packet connection to ", destination)
	return h.uotClient.ListenPacket(ctx, destination)
}
