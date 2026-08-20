package constant

import (
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/service/manager/constant"
)

type BandwidthLimiterManager interface {
	AddBandwidthLimiterStrategyManager(outbound adapter.Outbound) error
	GetBandwidthLimiterStrategyManager(tag string) (BandwidthLimiterStrategyManager, bool)
	GetBandwidthLimiterStrategyManagerTags() []string
}

type BandwidthLimiterStrategyManager interface {
	UpdateBandwidthLimiter(limiter C.BandwidthLimiter)
	UpdateBandwidthLimiters(limiter []C.BandwidthLimiter)
	DeleteBandwidthLimiter(username string)
}
