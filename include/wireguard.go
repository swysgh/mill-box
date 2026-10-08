//go:build with_wireguard

package include

import (
	"github.com/swysgh/mill-box/adapter/endpoint"
	"github.com/swysgh/mill-box/protocol/wireguard"
)

func registerWireGuardEndpoint(registry *endpoint.Registry) {
	wireguard.RegisterEndpoint(registry)
}
