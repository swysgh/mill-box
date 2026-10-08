//go:build with_quic

package include

import (
	"github.com/swysgh/mill-box/adapter/inbound"
	"github.com/swysgh/mill-box/adapter/outbound"
	"github.com/swysgh/mill-box/adapter/service"
	"github.com/swysgh/mill-box/dns"
	"github.com/swysgh/mill-box/dns/transport/quic"
	"github.com/swysgh/mill-box/protocol/hysteria"
	"github.com/swysgh/mill-box/protocol/hysteria2"
	_ "github.com/swysgh/mill-box/protocol/naive/quic"
	"github.com/swysgh/mill-box/protocol/tuic"
	_ "github.com/swysgh/mill-box/transport/v2rayquic"
)

func registerQUICInbounds(registry *inbound.Registry) {
	hysteria.RegisterInbound(registry)
	tuic.RegisterInbound(registry)
	hysteria2.RegisterInbound(registry)
}

func registerQUICOutbounds(registry *outbound.Registry) {
	hysteria.RegisterOutbound(registry)
	tuic.RegisterOutbound(registry)
	hysteria2.RegisterOutbound(registry)
}

func registerQUICTransports(registry *dns.TransportRegistry) {
	quic.RegisterTransport(registry)
	quic.RegisterHTTP3Transport(registry)
}

func registerQUICServices(registry *service.Registry) {
	hysteria2.RegisterRealmService(registry)
}
