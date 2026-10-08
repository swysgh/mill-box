//go:build with_naive_outbound

package include

import (
	"github.com/swysgh/mill-box/adapter/outbound"
	"github.com/swysgh/mill-box/protocol/naive"
)

func registerNaiveOutbound(registry *outbound.Registry) {
	naive.RegisterOutbound(registry)
}
