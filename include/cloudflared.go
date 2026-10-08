//go:build with_cloudflared

package include

import (
	"github.com/swysgh/mill-box/adapter/inbound"
	"github.com/swysgh/mill-box/protocol/cloudflare"
)

func registerCloudflaredInbound(registry *inbound.Registry) {
	cloudflare.RegisterInbound(registry)
}
