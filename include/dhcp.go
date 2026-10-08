//go:build with_dhcp

package include

import (
	"github.com/swysgh/mill-box/dns"
	"github.com/swysgh/mill-box/dns/transport/dhcp"
)

func registerDHCPTransport(registry *dns.TransportRegistry) {
	dhcp.RegisterTransport(registry)
}
