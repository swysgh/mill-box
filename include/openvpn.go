//go:build with_openvpn

package include

import (
	"github.com/swysgh/mill-box/adapter/endpoint"
	"github.com/swysgh/mill-box/dns"
	"github.com/swysgh/mill-box/protocol/openvpn"
)

func registerOpenVPNEndpoints(registry *endpoint.Registry) {
	openvpn.RegisterEndpoint(registry)
}

func registerOpenVPNDNSTransport(registry *dns.TransportRegistry) {
	openvpn.RegisterDNSTransport(registry)
}
