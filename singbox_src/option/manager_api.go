package option

import (
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badjson"
)

type _ManagerAPIOptions struct {
	APIType       string                  `json:"api_type"`
	ProtocolType  string                  `json:"protocol_type"`
	ServerOptions ManagerAPIServerOptions `json:"-"`
	ClientOptions ManagerAPIClientOptions `json:"-"`
}

type ManagerAPIOptions _ManagerAPIOptions

func (o ManagerAPIOptions) MarshalJSON() ([]byte, error) {
	var v any
	switch o.APIType {
	case C.ManagerAPIServer:
		v = o.ServerOptions
	case C.ManagerAPIClient:
		v = o.ClientOptions
	case "":
		return nil, E.New("missing api type")
	default:
		return nil, E.New("unknown api type: " + o.APIType)
	}
	return badjson.MarshallObjects(_ManagerAPIOptions(o), v)
}

func (o *ManagerAPIOptions) UnmarshalJSON(bytes []byte) error {
	err := json.Unmarshal(bytes, (*_ManagerAPIOptions)(o))
	if err != nil {
		return err
	}
	var v any
	switch o.APIType {
	case C.ManagerAPIServer:
		v = &o.ServerOptions
	case C.ManagerAPIClient:
		v = &o.ClientOptions
	case "":
		return E.New("missing api type")
	default:
		return E.New("unknown api type: " + o.APIType)
	}

	err = badjson.UnmarshallExcluded(bytes, (*_ManagerAPIOptions)(o), v)
	if err != nil {
		return err
	}
	return nil
}

type ManagerAPIServerOptions struct {
	ListenOptions
	InboundTLSOptionsContainer
	Manager string                 `json:"manager"`
	APIKey  string                 `json:"api_key"`
	CORS    *ManagerAPICORSOptions `json:"cors,omitempty"`
}

type ManagerAPICORSOptions struct {
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	ExposedHeaders []string `json:"exposed_headers,omitempty"`
	MaxAge         int      `json:"max_age,omitempty"`
}

type ManagerAPIClientOptions struct {
	DialerOptions
	ServerOptions
	OutboundTLSOptionsContainer
	APIKey string `json:"api_key"`
}
