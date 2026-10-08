package include

import (
	"context"

	"github.com/swysgh/mill-box"
	"github.com/swysgh/mill-box/adapter"
	"github.com/swysgh/mill-box/adapter/certificate"
	"github.com/swysgh/mill-box/adapter/endpoint"
	"github.com/swysgh/mill-box/adapter/inbound"
	"github.com/swysgh/mill-box/adapter/outbound"
	"github.com/swysgh/mill-box/adapter/service"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/dns"
	"github.com/swysgh/mill-box/dns/transport"
	"github.com/swysgh/mill-box/dns/transport/fakeip"
	"github.com/swysgh/mill-box/dns/transport/hosts"
	"github.com/swysgh/mill-box/dns/transport/local"
	"github.com/swysgh/mill-box/dns/transport/mdns"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"
	"github.com/swysgh/mill-box/protocol/anoiss"
	"github.com/swysgh/mill-box/protocol/anytls"
	"github.com/swysgh/mill-box/protocol/block"
	"github.com/swysgh/mill-box/protocol/bridge"
	"github.com/swysgh/mill-box/protocol/direct"
	"github.com/swysgh/mill-box/protocol/group"
	"github.com/swysgh/mill-box/protocol/http"
	"github.com/swysgh/mill-box/protocol/masque"
	"github.com/swysgh/mill-box/protocol/mixed"
	"github.com/swysgh/mill-box/protocol/naive"
	"github.com/swysgh/mill-box/protocol/redirect"
	"github.com/swysgh/mill-box/protocol/shadowsocks"
	"github.com/swysgh/mill-box/protocol/shadowtls"
	"github.com/swysgh/mill-box/protocol/snell"
	"github.com/swysgh/mill-box/protocol/socks"
	"github.com/swysgh/mill-box/protocol/ssh"
	"github.com/swysgh/mill-box/protocol/tor"
	"github.com/swysgh/mill-box/protocol/trojan"
	"github.com/swysgh/mill-box/protocol/tun"
	"github.com/swysgh/mill-box/protocol/vless"
	"github.com/swysgh/mill-box/protocol/vmess"
	"github.com/swysgh/mill-box/service/api"
	originca "github.com/swysgh/mill-box/service/origin_ca"
	"github.com/swysgh/mill-box/service/resolved"
	"github.com/swysgh/mill-box/service/ssmapi"

	E "github.com/sagernet/sing/common/exceptions"
)

func Context(ctx context.Context) context.Context {
	return box.Context(ctx, InboundRegistry(), OutboundRegistry(), EndpointRegistry(), DNSTransportRegistry(), ServiceRegistry(), CertificateProviderRegistry())
}

func InboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()

	tun.RegisterInbound(registry)
	redirect.RegisterRedirect(registry)
	redirect.RegisterTProxy(registry)
	direct.RegisterInbound(registry)

	socks.RegisterInbound(registry)
	http.RegisterInbound(registry)
	mixed.RegisterInbound(registry)

	shadowsocks.RegisterInbound(registry)
	snell.RegisterInbound(registry)
	vmess.RegisterInbound(registry)
	trojan.RegisterInbound(registry)
	naive.RegisterInbound(registry)
	shadowtls.RegisterInbound(registry)
	vless.RegisterInbound(registry)
	anytls.RegisterInbound(registry)
	anoiss.RegisterInbound(registry)

	registerQUICInbounds(registry)
	registerCloudflaredInbound(registry)
	registerTailcatInbound(registry)
	registerStubForRemovedInbounds(registry)

	return registry
}

func OutboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()

	direct.RegisterOutbound(registry)
	bridge.RegisterOutbound(registry)

	block.RegisterOutbound(registry)

	group.RegisterSelector(registry)
	group.RegisterURLTest(registry)

	socks.RegisterOutbound(registry)
	http.RegisterOutbound(registry)
	shadowsocks.RegisterOutbound(registry)
	snell.RegisterOutbound(registry)
	vmess.RegisterOutbound(registry)
	trojan.RegisterOutbound(registry)
	registerNaiveOutbound(registry)
	tor.RegisterOutbound(registry)
	ssh.RegisterOutbound(registry)
	shadowtls.RegisterOutbound(registry)
	vless.RegisterOutbound(registry)
	anytls.RegisterOutbound(registry)
	anoiss.RegisterOutbound(registry)

	registerQUICOutbounds(registry)
	registerTailcatOutbound(registry)
	registerStubForRemovedOutbounds(registry)

	return registry
}

func EndpointRegistry() *endpoint.Registry {
	registry := endpoint.NewRegistry()

	registerWireGuardEndpoint(registry)
	registerOpenConnectEndpoint(registry)
	registerOpenVPNEndpoints(registry)
	masque.RegisterEndpoint(registry)
	registerTailscaleEndpoint(registry)

	return registry
}

func DNSTransportRegistry() *dns.TransportRegistry {
	registry := dns.NewTransportRegistry()

	transport.RegisterTCP(registry)
	transport.RegisterUDP(registry)
	transport.RegisterTLS(registry)
	transport.RegisterHTTPS(registry)
	hosts.RegisterTransport(registry)
	local.RegisterTransport(registry)
	mdns.RegisterTransport(registry)
	fakeip.RegisterTransport(registry)
	resolved.RegisterTransport(registry)

	registerQUICTransports(registry)
	registerDHCPTransport(registry)
	registerTailscaleTransport(registry)
	registerOpenConnectDNSTransport(registry)
	registerOpenVPNDNSTransport(registry)

	return registry
}

func ServiceRegistry() *service.Registry {
	registry := service.NewRegistry()

	api.RegisterService(registry)
	resolved.RegisterService(registry)
	ssmapi.RegisterService(registry)

	registerQUICServices(registry)
	registerDERPService(registry)
	registerCCMService(registry)
	registerOCMService(registry)
	registerOOMKillerService(registry)
	registerUSBIPServices(registry)

	return registry
}

func CertificateProviderRegistry() *certificate.Registry {
	registry := certificate.NewRegistry()

	registerACMECertificateProvider(registry)
	registerTailscaleCertificateProvider(registry)
	originca.RegisterCertificateProvider(registry)

	return registry
}

func registerStubForRemovedInbounds(registry *inbound.Registry) {
	inbound.Register[option.ShadowsocksInboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (adapter.Inbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in sing-box 1.6.0")
	})
}

func registerStubForRemovedOutbounds(registry *outbound.Registry) {
	outbound.Register[option.ShadowsocksROutboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksROutboundOptions) (adapter.Outbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in sing-box 1.6.0")
	})
	outbound.Register[option.StubOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Outbound, error) {
		return nil, E.New("WireGuard outbound is deprecated in sing-box 1.11.0 and removed in sing-box 1.13.0, use WireGuard endpoint instead")
	})
}
