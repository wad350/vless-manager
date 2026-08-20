package node_manager_api

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/node_manager_api/client"
	"github.com/sagernet/sing-box/service/node_manager_api/server"
	E "github.com/sagernet/sing/common/exceptions"
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.NodeManagerAPIOptions](registry, C.TypeNodeManagerAPI, NewService)
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.NodeManagerAPIOptions) (adapter.Service, error) {
	switch options.APIType {
	case C.NodeManagerAPIServer:
		return server.NewAPIServer(ctx, logger, tag, options.ServerOptions)
	case C.NodeManagerAPIClient:
		return client.NewAPIClient(ctx, logger, tag, options.ClientOptions)
	case "":
		return nil, E.New("missing api type")
	default:
		return nil, E.New("unknown api type: ", options.APIType)
	}
}
