//go:build !with_admin_panel

package admin_panel

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
	service.Register[option.AdminPanelServiceOptions](registry, C.TypeAdminPanel, func(ctx context.Context, logger log.ContextLogger, tag string, options option.AdminPanelServiceOptions) (adapter.Service, error) {
		return nil, E.New(`Admin panel is not included in this build, rebuild with -tags with_admin_panel`)
	})
}
