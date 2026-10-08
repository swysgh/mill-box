package include

import (
	"github.com/swysgh/mill-box/adapter/service"
	"github.com/swysgh/mill-box/service/oomkiller"
)

func registerOOMKillerService(registry *service.Registry) {
	oomkiller.RegisterService(registry)
}
