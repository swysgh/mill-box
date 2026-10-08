//go:build with_tailscale

package include

import (
	"github.com/swysgh/mill-box/adapter/certificate"
	"github.com/swysgh/mill-box/adapter/endpoint"
	"github.com/swysgh/mill-box/adapter/inbound"
	"github.com/swysgh/mill-box/adapter/outbound"
	"github.com/swysgh/mill-box/adapter/service"
	"github.com/swysgh/mill-box/dns"
	"github.com/swysgh/mill-box/protocol/tailscale"
	"github.com/swysgh/mill-box/service/derp"
)

func registerTailscaleEndpoint(registry *endpoint.Registry) {
	tailscale.RegisterEndpoint(registry)
}

func registerTailcatInbound(registry *inbound.Registry) {
	tailscale.RegisterTailcatInbound(registry)
}

func registerTailcatOutbound(registry *outbound.Registry) {
	tailscale.RegisterTailcatOutbound(registry)
}

func registerTailscaleTransport(registry *dns.TransportRegistry) {
	tailscale.RegistryTransport(registry)
}

func registerTailscaleCertificateProvider(registry *certificate.Registry) {
	tailscale.RegisterCertificateProvider(registry)
}

func registerDERPService(registry *service.Registry) {
	derp.Register(registry)
}
