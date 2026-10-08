//go:build with_ocm

package include

import (
	"github.com/swysgh/mill-box/adapter/service"
	"github.com/swysgh/mill-box/service/ocm"
)

func registerOCMService(registry *service.Registry) {
	ocm.RegisterService(registry)
}
