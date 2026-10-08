//go:build !with_clash_api

package include

import (
	"context"

	"github.com/swysgh/mill-box/adapter"
	"github.com/swysgh/mill-box/experimental"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"

	E "github.com/sagernet/sing/common/exceptions"
)

func init() {
	experimental.RegisterClashServerConstructor(func(ctx context.Context, logFactory log.ObservableFactory, options option.ClashAPIOptions) (adapter.LifecycleService, error) {
		return nil, E.New(`clash api is not included in this build, rebuild with -tags with_clash_api`)
	})
}
