package constant

import (
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/service/manager/constant"
)

type RateLimiterManager interface {
	AddRateLimiterStrategyManager(outbound adapter.Outbound) error
	GetRateLimiterStrategyManager(tag string) (RateLimiterStrategyManager, bool)
	GetRateLimiterStrategyManagerTags() []string
}

type RateLimiterStrategyManager interface {
	UpdateRateLimiter(limiter C.RateLimiter)
	UpdateRateLimiters(limiter []C.RateLimiter)
	DeleteRateLimiter(username string)
}
