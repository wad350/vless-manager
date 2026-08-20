package limiter

import (
	"context"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/protocol/limiter/rate"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

type ManagedRateStrategy interface {
	UpdateStrategies(strategies map[string]rate.RateStrategy)
}

type RateLimiterManager struct {
	ctx         context.Context
	nodeManager CM.NodeManager
	logger      log.ContextLogger

	managers map[string]*RateLimiterStrategyManager

	mtx sync.Mutex
}

func NewRateLimiterManager(ctx context.Context, nodeManager CM.NodeManager, logger log.ContextLogger) *RateLimiterManager {
	return &RateLimiterManager{
		ctx:         ctx,
		nodeManager: nodeManager,
		logger:      logger,
		managers:    make(map[string]*RateLimiterStrategyManager),
	}
}

func (m *RateLimiterManager) AddRateLimiterStrategyManager(outbound adapter.Outbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	limiter, ok := outbound.(*rate.Outbound)
	if !ok {
		return E.New("invalid rate limiter: ", outbound.Tag())
	}
	strategy, ok := limiter.GetStrategy().(ManagedRateStrategy)
	if !ok {
		return E.New("strategy for outbound ", outbound.Tag(), " is not manager")
	}
	m.managers[outbound.Tag()] = &RateLimiterStrategyManager{
		manager:       m,
		strategy:      strategy,
		strategiesMap: make(map[string]rate.RateStrategy),
	}
	return nil
}

func (m *RateLimiterManager) GetRateLimiterStrategyManager(tag string) (constant.RateLimiterStrategyManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	manager, ok := m.managers[tag]
	return manager, ok
}

func (m *RateLimiterManager) GetRateLimiterStrategyManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.managers))
	for tag := range m.managers {
		tags = append(tags, tag)
	}
	return tags
}

type RateLimiterStrategyManager struct {
	manager       *RateLimiterManager
	strategy      ManagedRateStrategy
	strategiesMap map[string]rate.RateStrategy

	mtx sync.Mutex
}

func (i *RateLimiterStrategyManager) postUpdate() {
	i.strategy.UpdateStrategies(i.strategiesMap)
}

func (i *RateLimiterStrategyManager) createStrategy(limiter CM.RateLimiter) (rate.RateStrategy, error) {
	interval, err := time.ParseDuration(limiter.Interval)
	if err != nil {
		return nil, err
	}
	return rate.CreateStrategy(limiter.Strategy, limiter.ConnectionType, int(limiter.Count), interval)
}

func (i *RateLimiterStrategyManager) UpdateRateLimiter(limiter CM.RateLimiter) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	strategy, err := i.createStrategy(limiter)
	if err != nil {
		i.manager.logger.ErrorContext(i.manager.ctx, err)
		return
	}
	i.strategiesMap[limiter.Username] = strategy
	i.postUpdate()
}

func (i *RateLimiterStrategyManager) UpdateRateLimiters(limiters []CM.RateLimiter) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.strategiesMap)
	newStrategiesMap := make(map[string]rate.RateStrategy)
	for _, limiter := range limiters {
		strategy, err := i.createStrategy(limiter)
		if err != nil {
			i.manager.logger.ErrorContext(i.manager.ctx, err)
			continue
		}
		newStrategiesMap[limiter.Username] = strategy
	}
	i.strategiesMap = newStrategiesMap
	i.postUpdate()
}

func (i *RateLimiterStrategyManager) DeleteRateLimiter(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.strategiesMap, username)
	i.postUpdate()
}
