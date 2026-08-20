package connection

import (
	"context"
	"strconv"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/onclose"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

type (
	ConnIDGetter = func(context.Context, *adapter.InboundContext) (string, bool)
	LockIDGetter = func(string) (onclose.CloseHandlerFunc, context.Context, error)

	ConnectionStrategy interface {
		request(ctx context.Context, metadata *adapter.InboundContext) (onClose onclose.CloseHandlerFunc, lockCtx context.Context, err error)
	}
)

type DefaultConnectionStrategy struct {
	connIDGetter ConnIDGetter
	lockIDGetter LockIDGetter

	mtx sync.Mutex
}

func NewDefaultConnectionStrategy(connIDGetter ConnIDGetter, lockIDGetter LockIDGetter) *DefaultConnectionStrategy {
	outbound := &DefaultConnectionStrategy{
		connIDGetter: connIDGetter,
		lockIDGetter: lockIDGetter,
	}
	return outbound
}

func (s *DefaultConnectionStrategy) request(ctx context.Context, metadata *adapter.InboundContext) (onclose.CloseHandlerFunc, context.Context, error) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	id, ok := s.connIDGetter(ctx, metadata)
	if !ok {
		return nil, nil, E.New("id not found")
	}
	return s.lockIDGetter(id)
}

type UsersConnectionStrategy struct {
	strategies map[string]ConnectionStrategy
	mtx        sync.Mutex
}

func NewUsersConnectionStrategy(strategies map[string]ConnectionStrategy) *UsersConnectionStrategy {
	return &UsersConnectionStrategy{
		strategies: strategies,
	}
}

func (s *UsersConnectionStrategy) request(ctx context.Context, metadata *adapter.InboundContext) (onclose.CloseHandlerFunc, context.Context, error) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	var user string
	if metadata != nil {
		user = metadata.User
	}
	strategy, ok := s.strategies[user]
	if ok {
		return strategy.request(ctx, metadata)
	}
	return nil, nil, E.New("user strategy not found: ", user)
}

type cancelEntry struct {
	cancel context.CancelFunc
}

type ManagerConnectionStrategy struct {
	strategies map[string]ConnectionStrategy
	cancels    map[string][]*cancelEntry

	mtx sync.Mutex
}

func NewManagerConnectionStrategy() *ManagerConnectionStrategy {
	return &ManagerConnectionStrategy{
		strategies: make(map[string]ConnectionStrategy),
		cancels:    make(map[string][]*cancelEntry),
	}
}

func (s *ManagerConnectionStrategy) request(ctx context.Context, metadata *adapter.InboundContext) (onclose.CloseHandlerFunc, context.Context, error) {
	s.mtx.Lock()
	var user string
	if metadata != nil {
		user = metadata.User
	}
	strategy, ok := s.strategies[user]
	if !ok {
		s.mtx.Unlock()
		return nil, nil, E.New("user strategy not found: ", user)
	}
	s.mtx.Unlock()
	onClose, _, err := strategy.request(ctx, metadata)
	if err != nil {
		return nil, nil, err
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	entry := &cancelEntry{cancel: cancel}
	s.mtx.Lock()
	s.cancels[user] = append(s.cancels[user], entry)
	s.mtx.Unlock()
	originalOnClose := onClose
	wrappedOnClose := func() {
		s.mtx.Lock()
		entries := s.cancels[user]
		for i, e := range entries {
			if e == entry {
				s.cancels[user] = append(entries[:i], entries[i+1:]...)
				break
			}
		}
		s.mtx.Unlock()
		cancel()
		if originalOnClose != nil {
			originalOnClose()
		}
	}
	return wrappedOnClose, cancelCtx, nil
}

func (s *ManagerConnectionStrategy) UpdateStrategies(strategies map[string]ConnectionStrategy) {
	s.mtx.Lock()
	var entries []*cancelEntry
	for user, cancels := range s.cancels {
		if _, exists := strategies[user]; !exists {
			entries = append(entries, cancels...)
			delete(s.cancels, user)
		}
	}
	s.strategies = strategies
	s.mtx.Unlock()
	for _, entry := range entries {
		entry.cancel()
	}
}

type BypassConnectionStrategy struct{}

func NewBypassConnectionStrategy() *BypassConnectionStrategy {
	return &BypassConnectionStrategy{}
}

func (s *BypassConnectionStrategy) request(ctx context.Context, metadata *adapter.InboundContext) (onclose.CloseHandlerFunc, context.Context, error) {
	return func() {}, nil, nil
}

func CreateStrategy(strategy string, connectionType string, lockIDGetter LockIDGetter) (ConnectionStrategy, error) {
	switch strategy {
	case "connection":
		var connIDGetter ConnIDGetter
		switch connectionType {
		case "hwid":
			connIDGetter = func(ctx context.Context, metadata *adapter.InboundContext) (string, bool) {
				id, ok := ctx.Value("hwid").(string)
				return id, ok
			}
		case "mux":
			connIDGetter = func(ctx context.Context, metadata *adapter.InboundContext) (string, bool) {
				id, ok := log.MuxIDFromContext(ctx)
				if !ok {
					return "", ok
				}
				return strconv.FormatUint(uint64(id.ID), 10), ok
			}
		case "source_ip":
			connIDGetter = func(ctx context.Context, metadata *adapter.InboundContext) (string, bool) {
				return metadata.Source.IPAddr().String(), true
			}
		case "default", "":
			connIDGetter = func(ctx context.Context, metadata *adapter.InboundContext) (string, bool) {
				id, ok := log.IDFromContext(ctx)
				if !ok {
					return "", ok
				}
				return strconv.FormatUint(uint64(id.ID), 10), ok
			}
		default:
			return nil, E.New("connection type not found: ", connectionType)
		}
		return NewDefaultConnectionStrategy(connIDGetter, lockIDGetter), nil
	case "bypass":
		return NewBypassConnectionStrategy(), nil
	default:
		return nil, E.New("strategy not found: ", strategy)
	}
}
