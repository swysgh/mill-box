//go:build with_ccm && (!darwin || cgo)

package include

import (
	"github.com/swysgh/mill-box/adapter/service"
	"github.com/swysgh/mill-box/service/ccm"
)

func registerCCMService(registry *service.Registry) {
	ccm.RegisterService(registry)
}
