package ush

import (
	"context"
	"net"

	"github.com/swysgh/mill-box/adapter"
	"github.com/swysgh/mill-box/adapter/inbound"
	"github.com/swysgh/mill-box/common/listener"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.USHInboundOptions](registry, C.TypeUSH, NewInbound)
}

var _ adapter.ConnectionHandler = (*Inbound)(nil)

// Inbound is the server half of ush: a UoT listener with no authentication
// layer, because it is reached through the sshd in front of it. Every accepted
// connection is a UoT stream, so the first bytes are read as a UoT request.
type Inbound struct {
	inbound.Adapter
	router   adapter.ConnectionRouterEx
	logger   logger.ContextLogger
	listener *listener.Listener
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.USHInboundOptions) (adapter.Inbound, error) {
	in := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeUSH, tag),
		router:  router,
		logger:  logger,
	}
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
	h.logger.Info("ush inbound starting: unauthenticated UoT listener, only reachable through the SSH tunnel in front of it")
	err := h.listener.Start()
	if err != nil {
		return err
	}
	scope.Add(h.listener.Close)
	return nil
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	request, err := uot.ReadRequest(conn)
	if err != nil {
		err = E.Cause(err, "read UoT request from ", metadata.Source)
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, err)
		return
	}
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	metadata.Destination = request.Destination
	metadata.Network = N.NetworkUDP
	h.logger.InfoContext(ctx, "inbound UoT packet connection to ", request.Destination)
	h.router.RoutePacketConnectionEx(ctx, uot.NewConn(conn, *request), metadata, onClose)
}
