package anoiss

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/flynn/noise"
	"github.com/sagernet/sing-anytls"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.AnoissInboundOptions](registry, C.TypeAnoiss, NewInbound)
}

type Inbound struct {
	inbound.Adapter
	router           adapter.ConnectionRouterEx
	logger           logger.ContextLogger
	listener         *listener.Listener
	service          *anytls.MultiService[string]
	staticKey        noise.DHKey
	clientPubKeys    map[string]bool
	allowAnyClient   bool
	psk              []byte
	handshakeTimeout time.Duration
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AnoissInboundOptions) (adapter.Inbound, error) {
	in := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeAnoiss, tag),
		router:  uot.NewRouter(router, logger),
		logger:  logger,
	}

	// Parse server private key
	if options.ServerPrivateKey == "" {
		return nil, E.New("anoiss: server_private_key is required")
	}
	privBytes, err := decodeKey("server_private_key", options.ServerPrivateKey)
	if err != nil {
		return nil, err
	}
	// Derive public key from private key using DH25519
	pub, err := pubFromPriv(privBytes)
	if err != nil {
		return nil, err
	}
	in.staticKey = noise.DHKey{Private: privBytes, Public: pub}

	// Parse client public keys whitelist
	in.allowAnyClient = options.AllowAnyClient
	if in.allowAnyClient {
		logger.Warn("anoiss: allow_any_client is enabled, accepting any client (for testing only)")
	}
	in.clientPubKeys = make(map[string]bool)
	for _, pk := range options.ClientPublicKeys {
		decoded, err := decodeKey("client_public_keys", pk)
		if err != nil {
			return nil, err
		}
		in.clientPubKeys[string(decoded)] = true
	}
	if !in.allowAnyClient && len(in.clientPubKeys) == 0 {
		return nil, E.New("anoiss: either client_public_keys or allow_any_client must be set")
	}

	// Parse PSK
	if options.PreSharedKey != "" {
		in.psk, err = decodeKey("pre_shared_key", options.PreSharedKey)
		if err != nil {
			return nil, err
		}
	}

	in.handshakeTimeout = options.HandshakeTimeout.Build()

	// Padding scheme
	var paddingScheme []byte
	if len(options.PaddingScheme) > 0 {
		paddingScheme = []byte(strings.Join(options.PaddingScheme, "\n"))
	}

	service, err := anytls.NewMultiService[string](anytls.ServiceOptions{
		PaddingScheme: paddingScheme,
		Handler:       (*inboundHandler)(in),
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}
	err = service.UpdateUsers(
		common.Map(options.Users, func(it option.AnoissUser) string { return it.Name }),
		common.Map(options.Users, func(it option.AnoissUser) string { return it.Password }),
	)
	if err != nil {
		return nil, err
	}
	in.service = service
	in.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: in,
	})
	return in, nil
}

func (h *Inbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}

	// Log startup info (§6.3)
	fingerprint := sha256.Sum256(h.staticKey.Public)
	h.logger.Info("anoiss inbound starting",
		" listen=", fmt.Sprint(h.listener.ListenOptions().Listen),
		" server_pubkey_fingerprint=", hex.EncodeToString(fingerprint[:8]),
		" psk=", len(h.psk) > 0,
		" client_whitelist_count=", len(h.clientPubKeys),
		" allow_any_client=", h.allowAnyClient,
		" handshake_timeout=", h.handshakeTimeout.String(),
	)

	err := h.listener.Start()
	if err != nil {
		return err
	}
	scope.Add(h.listener.Close)
	return nil
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	timeout := h.handshakeTimeout
	noiseConn, peerPub, err := ServerHandshake(conn, h.staticKey, h.psk, timeout)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": Noise handshake"))
		return
	}

	// Verify client public key
	if !h.clientAllowed(peerPub) {
		noiseConn.Close()
		if onClose != nil {
			onClose(nil)
		}
		h.logger.WarnContext(ctx, "anoiss: rejected client with unknown public key: ",
			base64.RawURLEncoding.EncodeToString(peerPub),
			" from ", metadata.Source)
		return
	}

	err = h.service.NewConnection(adapter.WithContext(ctx, &metadata), noiseConn, metadata.Source, onClose)
	if err != nil {
		N.CloseOnHandshakeFailure(noiseConn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
	}
}

func (h *Inbound) clientAllowed(peerPub []byte) bool {
	return h.allowAnyClient || h.clientPubKeys[string(peerPub)]
}

type inboundHandler Inbound

func (h *inboundHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.Source = source
	metadata.Destination = destination.Unwrap()
	if userName, _ := auth.UserFromContext[string](ctx); userName != "" {
		metadata.User = userName
		h.logger.InfoContext(ctx, "[", userName, "] inbound connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

// pubFromPriv derives the X25519 public key from a private key using noise.DH25519.
func pubFromPriv(priv []byte) ([]byte, error) {
	r := bytes.NewReader(priv)
	kp, err := noise.DH25519.GenerateKeypair(r)
	if err != nil {
		return nil, E.Cause(err, "anoiss: derive public key from private key")
	}
	return kp.Public, nil
}
