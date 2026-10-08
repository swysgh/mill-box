//go:build !ios

package rule

import (
	"context"

	"github.com/swysgh/mill-box/option"

	"github.com/sagernet/sing/common/logger"
)

func mmapRuleSet(ctx context.Context, logger logger.Logger, tag string, ruleSet option.PlainRuleSetCompat) option.PlainRuleSetCompat {
	return ruleSet
}
