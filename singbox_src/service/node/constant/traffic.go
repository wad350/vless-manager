package constant

import (
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/service/manager/constant"
)

type TrafficLimiterManager interface {
	AddTrafficLimiterStrategyManager(outbound adapter.Outbound) error
	GetTrafficLimiterStrategyManager(tag string) (TrafficLimiterStrategyManager, bool)
	GetTrafficLimiterStrategyManagerTags() []string
}

type TrafficLimiterStrategyManager interface {
	UpdateTrafficLimiter(limiter C.TrafficLimiter)
	UpdateTrafficLimiters(limiter []C.TrafficLimiter)
	DeleteTrafficLimiter(username string)
}
