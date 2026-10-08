//go:build !with_openconnect

package include

import (
	"context"

	"github.com/swysgh/mill-box/adapter"
	"github.com/swysgh/mill-box/adapter/endpoint"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/dns"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"

	E "github.com/sagernet/sing/common/exceptions"
)

func registerOpenConnectEndpoint(registry *endpoint.Registry) {
	endpoint.Register[option.OpenConnectEndpointOptions](registry, C.TypeOpenConnect, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.OpenConnectEndpointOptions) (adapter.Endpoint, error) {
		return nil, E.New(`OpenConnect is not included in this build, rebuild with -tags with_openconnect`)
	})
}

func registerOpenConnectDNSTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.OpenConnectDNSServerOptions](registry, C.DNSTypeOpenConnect, func(ctx context.Context, logger log.ContextLogger, tag string, options option.OpenConnectDNSServerOptions) (adapter.DNSTransport, error) {
		return nil, E.New(`OpenConnect is not included in this build, rebuild with -tags with_openconnect`)
	})
}
