//go:build !with_ocm

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

func registerOCMService(registry *service.Registry) {
	service.Register[option.OCMServiceOptions](registry, C.TypeOCM, func(ctx context.Context, logger log.ContextLogger, tag string, options option.OCMServiceOptions) (adapter.Service, error) {
		return nil, E.New(`OCM is not included in this build, rebuild with -tags with_ocm`)
	})
}
