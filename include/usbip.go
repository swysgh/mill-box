//go:build with_usbip && (linux || (darwin && cgo) || windows)

package include

import (
	"github.com/swysgh/mill-box/adapter/service"
	"github.com/swysgh/mill-box/service/usbip"
)

func registerUSBIPServices(registry *service.Registry) {
	usbip.RegisterService(registry)
}
