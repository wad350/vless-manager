package limiter

import (
	"context"
	"slices"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/protocol/limiter/bandwidth"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

type ManagedBandwidthStrategy interface {
	UpdateStrategies(strategies map[string]bandwidth.BandwidthStrategy)
}

type BandwidthLimiterManager struct {
	ctx         context.Context
	nodeManager CM.NodeManager
	logger      log.ContextLogger

	managers map[string]*BandwidthLimiterStrategyManager

	mtx sync.Mutex
}

func NewBandwidthLimiterManager(ctx context.Context, nodeManager CM.NodeManager, logger log.ContextLogger) *BandwidthLimiterManager {
	return &BandwidthLimiterManager{
		ctx:         ctx,
		nodeManager: nodeManager,
		logger:      logger,
		managers:    make(map[string]*BandwidthLimiterStrategyManager),
	}
}

func (m *BandwidthLimiterManager) AddBandwidthLimiterStrategyManager(outbound adapter.Outbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	limiter, ok := outbound.(*bandwidth.Outbound)
	if !ok {
		return E.New("invalid bandwidth limiter: ", outbound.Tag())
	}
	strategy, ok := limiter.GetStrategy().(ManagedBandwidthStrategy)
	if !ok {
		return E.New("strategy for outbound ", outbound.Tag(), " is not manager")
	}
	m.managers[outbound.Tag()] = &BandwidthLimiterStrategyManager{
		manager:       m,
		strategy:      strategy,
		strategiesMap: make(map[string]bandwidth.BandwidthStrategy),
		limitersMap:   make(map[string]CM.BandwidthLimiter),
	}
	return nil
}

func (m *BandwidthLimiterManager) GetBandwidthLimiterStrategyManager(tag string) (constant.BandwidthLimiterStrategyManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	manager, ok := m.managers[tag]
	return manager, ok
}

func (m *BandwidthLimiterManager) GetBandwidthLimiterStrategyManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.managers))
	for tag := range m.managers {
		tags = append(tags, tag)
	}
	return tags
}

type BandwidthLimiterStrategyManager struct {
	manager       *BandwidthLimiterManager
	strategy      ManagedBandwidthStrategy
	strategiesMap map[string]bandwidth.BandwidthStrategy
	limitersMap   map[string]CM.BandwidthLimiter

	mtx sync.Mutex
}

func (i *BandwidthLimiterStrategyManager) postUpdate() {
	i.strategy.UpdateStrategies(i.strategiesMap)
}

func (i *BandwidthLimiterStrategyManager) UpdateBandwidthLimiter(limiter CM.BandwidthLimiter) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	if existing, ok := i.strategiesMap[limiter.Username]; ok {
		oldLimiter := i.limitersMap[limiter.Username]
		if isSameStrategy(oldLimiter, limiter) {
			if oldLimiter.RawSpeed != limiter.RawSpeed {
				if s, ok := existing.(bandwidth.SpeedUpdater); ok {
					s.SetSpeed(limiter.RawSpeed)
				}
			}
			i.limitersMap[limiter.Username] = limiter
			return
		}
	}
	strategy, err := bandwidth.CreateStrategy(limiter.Strategy, limiter.Mode, limiter.ConnectionType, limiter.RawSpeed, limiter.FlowKeys)
	if err != nil {
		i.manager.logger.ErrorContext(i.manager.ctx, err)
		return
	}
	i.strategiesMap[limiter.Username] = strategy
	i.limitersMap[limiter.Username] = limiter
	i.postUpdate()
}

func (i *BandwidthLimiterStrategyManager) UpdateBandwidthLimiters(limiters []CM.BandwidthLimiter) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.strategiesMap)
	newStrategiesMap := make(map[string]bandwidth.BandwidthStrategy)
	for _, limiter := range limiters {
		strategy, err := bandwidth.CreateStrategy(limiter.Strategy, limiter.Mode, limiter.ConnectionType, limiter.RawSpeed, limiter.FlowKeys)
		if err != nil {
			i.manager.logger.ErrorContext(i.manager.ctx, err)
			continue
		}
		newStrategiesMap[limiter.Username] = strategy
	}
	i.strategiesMap = newStrategiesMap
	i.postUpdate()
}

func (i *BandwidthLimiterStrategyManager) DeleteBandwidthLimiter(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.strategiesMap, username)
	i.postUpdate()
}

func isSameStrategy(a, b CM.BandwidthLimiter) bool {
	return a.Strategy == b.Strategy &&
		a.Mode == b.Mode &&
		a.ConnectionType == b.ConnectionType &&
		slices.Equal(a.FlowKeys, b.FlowKeys)
}
