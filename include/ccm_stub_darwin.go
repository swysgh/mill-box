//go:build with_ccm && darwin && !cgo

package include

import (
	"context"

	"github.com/swysgh/mill-box/adapter"
	"github.com/swysgh/mill-box/adapter/service"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"

	E "github.com/sagernet/sing/common/exceptions"
)

func registerCCMService(registry *service.Registry) {
	service.Register[option.CCMServiceOptions](registry, C.TypeCCM, func(ctx context.Context, logger log.ContextLogger, tag string, options option.CCMServiceOptions) (adapter.Service, error) {
		return nil, E.New(`CCM requires CGO on darwin, rebuild with CGO_ENABLED=1`)
	})
}
