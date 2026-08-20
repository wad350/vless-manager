//go:build !with_manager

package manager

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func RegisterService(registry *service.Registry) {
	service.Register[option.ManagerServiceOptions](registry, C.TypeManager, func(ctx context.Context, logger log.ContextLogger, tag string, options option.ManagerServiceOptions) (adapter.Service, error) {
		return nil, E.New(`Manager is not included in this build, rebuild with -tags with_manager`)
	})
}
