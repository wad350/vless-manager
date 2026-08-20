package connection

import (
	"context"
	"sync"

	"github.com/sagernet/sing-box/common/onclose"
	E "github.com/sagernet/sing/common/exceptions"
)

func NewDefaultLock(max uint32) LockIDGetter {
	locks := make(map[string]*uint32)
	mtx := sync.Mutex{}
	return func(id string) (onclose.CloseHandlerFunc, context.Context, error) {
		mtx.Lock()
		defer mtx.Unlock()
		handles, ok := locks[id]
		if !ok {
			if len(locks) == int(max) {
				return nil, nil, E.New("not enough free locks")
			}
			handles = new(uint32)
			locks[id] = handles
		}
		*handles++
		return func() {
			mtx.Lock()
			defer mtx.Unlock()
			*handles--
			if *handles == 0 {
				delete(locks, id)
			}
		}, nil, nil
	}
}
