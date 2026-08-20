package constant

import (
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/service/manager/constant"
)

type ConnectionLimiterManager interface {
	AddConnectionLimiterStrategyManager(outbound adapter.Outbound) error
	GetConnectionLimiterStrategyManager(tag string) (ConnectionLimiterStrategyManager, bool)
	GetConnectionLimiterStrategyManagerTags() []string
}

type ConnectionLimiterStrategyManager interface {
	UpdateConnectionLimiter(limiter C.ConnectionLimiter)
	UpdateConnectionLimiters(limiter []C.ConnectionLimiter)
	DeleteConnectionLimiter(username string)
}
