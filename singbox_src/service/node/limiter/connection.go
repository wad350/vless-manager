package limiter

import (
	"context"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/onclose"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/protocol/limiter/connection"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

type ManagedConnectionStrategy interface {
	UpdateStrategies(strategies map[string]connection.ConnectionStrategy)
}

type ConnectionLimiterManager struct {
	ctx         context.Context
	nodeManager CM.NodeManager
	logger      log.ContextLogger

	managers map[string]*ConnectionLimiterStrategyManager

	mtx sync.Mutex
}

func NewConnectionLimiterManager(ctx context.Context, nodeManager CM.NodeManager, logger log.ContextLogger) *ConnectionLimiterManager {
	return &ConnectionLimiterManager{
		ctx:         ctx,
		nodeManager: nodeManager,
		logger:      logger,
		managers:    make(map[string]*ConnectionLimiterStrategyManager),
	}
}

func (m *ConnectionLimiterManager) AddConnectionLimiterStrategyManager(outbound adapter.Outbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	limiter, ok := outbound.(*connection.Outbound)
	if !ok {
		return E.New("invalid connection limiter: ", outbound.Tag())
	}
	strategy, ok := limiter.GetStrategy().(ManagedConnectionStrategy)
	if !ok {
		return E.New("strategy for outbound ", outbound.Tag(), " is not manager")
	}
	m.managers[outbound.Tag()] = &ConnectionLimiterStrategyManager{
		manager:       m,
		strategy:      strategy,
		strategiesMap: make(map[string]connection.ConnectionStrategy),
	}
	return nil
}

func (m *ConnectionLimiterManager) GetConnectionLimiterStrategyManager(tag string) (constant.ConnectionLimiterStrategyManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	manager, ok := m.managers[tag]
	return manager, ok
}

func (m *ConnectionLimiterManager) GetConnectionLimiterStrategyManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.managers))
	for tag := range m.managers {
		tags = append(tags, tag)
	}
	return tags
}

type ConnectionLimiterStrategyManager struct {
	manager       *ConnectionLimiterManager
	strategy      ManagedConnectionStrategy
	strategiesMap map[string]connection.ConnectionStrategy
	tag           string

	mtx sync.Mutex
}

func (i *ConnectionLimiterStrategyManager) postUpdate() {
	i.strategy.UpdateStrategies(i.strategiesMap)
}

func (i *ConnectionLimiterStrategyManager) UpdateConnectionLimiter(limiter CM.ConnectionLimiter) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	lock, err := i.createLock(limiter)
	if err != nil {
		i.manager.logger.ErrorContext(i.manager.ctx, err)
		return
	}
	strategy, err := connection.CreateStrategy(limiter.Strategy, limiter.ConnectionType, lock)
	if err != nil {
		i.manager.logger.ErrorContext(i.manager.ctx, err)
		return
	}
	i.strategiesMap[limiter.Username] = strategy
	i.postUpdate()
}

func (i *ConnectionLimiterStrategyManager) UpdateConnectionLimiters(limiters []CM.ConnectionLimiter) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.strategiesMap)
	newStrategiesMap := make(map[string]connection.ConnectionStrategy)
	for _, limiter := range limiters {
		lock, err := i.createLock(limiter)
		if err != nil {
			i.manager.logger.ErrorContext(i.manager.ctx, err)
			continue
		}
		strategy, err := connection.CreateStrategy(limiter.Strategy, limiter.ConnectionType, lock)
		if err != nil {
			i.manager.logger.ErrorContext(i.manager.ctx, err)
			continue
		}
		newStrategiesMap[limiter.Username] = strategy
	}
	i.strategiesMap = newStrategiesMap
	i.postUpdate()
}

func (i *ConnectionLimiterStrategyManager) DeleteConnectionLimiter(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.strategiesMap, username)
	i.postUpdate()
}

func (i *ConnectionLimiterStrategyManager) createLock(limiter CM.ConnectionLimiter) (connection.LockIDGetter, error) {
	switch limiter.LockType {
	case "manager":
		return i.newManagerLock(limiter.ID), nil
	case "default", "":
		return connection.NewDefaultLock(limiter.Count), nil
	default:
		return nil, E.New("unknown lock type \"", limiter.LockType, "\"")
	}
}

type ManagerLock struct {
	handleId string
	ctx      context.Context
	cancel   context.CancelFunc
	handles  uint32
}

func (i *ConnectionLimiterStrategyManager) newManagerLock(limiterId int) connection.LockIDGetter {
	conns := make(map[string]*ManagerLock)
	mtx := sync.Mutex{}
	return func(id string) (onclose.CloseHandlerFunc, context.Context, error) {
		mtx.Lock()
		defer mtx.Unlock()
		conn, ok := conns[id]
		if !ok {
			nodeManager := i.manager.nodeManager
			handleId, err := nodeManager.AcquireLock(limiterId, id)
			if err != nil {
				return nil, nil, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Second * 5):
						err := nodeManager.RefreshLock(limiterId, id, handleId)
						if err != nil {
							i.manager.logger.ErrorContext(ctx, "failed to refresh lock: ", err)
							cancel()
							return
						}
					}
				}
			}()
			conn = &ManagerLock{
				handleId: handleId,
				ctx:      ctx,
				cancel:   cancel,
			}
			conns[id] = conn
		}
		conn.handles++
		return func() {
			mtx.Lock()
			defer mtx.Unlock()
			conn.handles--
			if conn.handles == 0 {
				conn.cancel()
				if err := i.manager.nodeManager.ReleaseLock(limiterId, id, conn.handleId); err != nil {
					i.manager.logger.ErrorContext(i.manager.ctx, "failed to release lock: ", err)
				}
				delete(conns, id)
			}
		}, conn.ctx, nil
	}
}
