//go:build with_openconnect

package include

import (
	"github.com/swysgh/mill-box/adapter/endpoint"
	"github.com/swysgh/mill-box/dns"
	"github.com/swysgh/mill-box/protocol/openconnect"
)

func registerOpenConnectEndpoint(registry *endpoint.Registry) {
	openconnect.RegisterEndpoint(registry)
}

func registerOpenConnectDNSTransport(registry *dns.TransportRegistry) {
	openconnect.RegisterDNSTransport(registry)
}
