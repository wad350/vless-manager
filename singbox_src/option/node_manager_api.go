package option

import (
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badjson"
	"github.com/sagernet/sing/common/json/badoption"
)

type _NodeManagerAPIOptions struct {
	APIType       string                      `json:"api_type"`
	ServerOptions NodeManagerAPIServerOptions `json:"-"`
	ClientOptions NodeManagerAPIClientOptions `json:"-"`
}

type NodeManagerAPIOptions _NodeManagerAPIOptions

func (o NodeManagerAPIOptions) MarshalJSON() ([]byte, error) {
	var v any
	switch o.APIType {
	case C.NodeManagerAPIServer:
		v = o.ServerOptions
	case C.NodeManagerAPIClient:
		v = o.ClientOptions
	case "":
		return nil, E.New("missing api type")
	default:
		return nil, E.New("unknown api type: " + o.APIType)
	}
	return badjson.MarshallObjects(_NodeManagerAPIOptions(o), v)
}

func (o *NodeManagerAPIOptions) UnmarshalJSON(bytes []byte) error {
	err := json.Unmarshal(bytes, (*_NodeManagerAPIOptions)(o))
	if err != nil {
		return err
	}
	var v any
	switch o.APIType {
	case C.NodeManagerAPIServer:
		v = &o.ServerOptions
	case C.NodeManagerAPIClient:
		v = &o.ClientOptions
	case "":
		return E.New("missing api type")
	default:
		return E.New("unknown api type: " + o.APIType)
	}

	err = badjson.UnmarshallExcluded(bytes, (*_NodeManagerAPIOptions)(o), v)
	if err != nil {
		return err
	}
	return nil
}

type NodeManagerAPIServerOptions struct {
	ListenOptions
	InboundTLSOptionsContainer
	Manager          string             `json:"manager"`
	APIKey           string             `json:"api_key"`
	KeepAlive        badoption.Duration `json:"keep_alive,omitempty"`
	KeepAliveTimeout badoption.Duration `json:"keep_alive_timeout,omitempty"`
}

type NodeManagerAPIClientOptions struct {
	DialerOptions
	ServerOptions
	OutboundTLSOptionsContainer
	APIKey string `json:"api_key"`
}
