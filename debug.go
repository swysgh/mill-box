package box

import (
	"runtime/debug"

	"github.com/swysgh/mill-box/option"
)

func applyDebugOptions(options option.DebugOptions) {
	if options.GCPercent != nil {
		debug.SetGCPercent(*options.GCPercent)
	}
	if options.MaxStack != nil {
		debug.SetMaxStack(*options.MaxStack)
	}
	if options.MaxThreads != nil {
		debug.SetMaxThreads(*options.MaxThreads)
	}
	if options.PanicOnFault != nil {
		debug.SetPanicOnFault(*options.PanicOnFault)
	}
	if options.TraceBack != "" {
		debug.SetTraceback(options.TraceBack)
	}
	if options.MemoryLimit.Value() != 0 {
		debug.SetMemoryLimit(int64(float64(options.MemoryLimit.Value()) / 1.5))
	}
}
