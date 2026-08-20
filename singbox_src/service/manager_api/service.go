package manager_api

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	grpcClient "github.com/sagernet/sing-box/service/manager_api/grpc/client"
	grpcServer "github.com/sagernet/sing-box/service/manager_api/grpc/server"
	httpClient "github.com/sagernet/sing-box/service/manager_api/http/client"
	httpServer "github.com/sagernet/sing-box/service/manager_api/http/server"
	E "github.com/sagernet/sing/common/exceptions"
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.ManagerAPIOptions](registry, C.TypeManagerAPI, NewService)
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.ManagerAPIOptions) (adapter.Service, error) {
	switch options.APIType {
	case C.ManagerAPIServer:
		switch options.ProtocolType {
		case C.ManagerAPIProtocolHTTP:
			return httpServer.NewAPIServer(ctx, logger, tag, options.ServerOptions)
		case C.ManagerAPIProtocolGrpc:
			return grpcServer.NewServer(ctx, logger, tag, options.ServerOptions)
		case "":
			return nil, E.New("missing protocol type")
		default:
			return nil, E.New("unknown protocol type: ", options.ProtocolType)
		}
	case C.ManagerAPIClient:
		switch options.ProtocolType {
		case C.ManagerAPIProtocolHTTP:
			return httpClient.NewClient(ctx, logger, tag, options.ClientOptions)
		case C.ManagerAPIProtocolGrpc:
			return grpcClient.NewClient(ctx, logger, tag, options.ClientOptions)
		case "":
			return nil, E.New("missing protocol type")
		default:
			return nil, E.New("unknown protocol type: ", options.ProtocolType)
		}
	case "":
		return nil, E.New("missing api type")
	default:
		return nil, E.New("unknown api type: ", options.APIType)
	}
}
