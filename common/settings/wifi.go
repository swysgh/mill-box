package settings

import (
	"context"

	"github.com/swysgh/mill-box/adapter"
)

type WIFIMonitor interface {
	ReadWIFIState(ctx context.Context) adapter.WIFIState
	Start() error
	Close() error
}
