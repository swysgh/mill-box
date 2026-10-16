package libbox

import (
	"os"
	_ "unsafe"
)

// https://github.com/SagerNet/sing-box/issues/3233
// https://github.com/golang/go/issues/70508

//go:linkname checkPidfdOnce os.checkPidfdOnce
var checkPidfdOnce func() error

func init() {
	checkPidfdOnce = func() error {
		return os.ErrInvalid
	}
}
