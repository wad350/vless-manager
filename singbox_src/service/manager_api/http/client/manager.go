package client

import (
	"net/http"

	CM "github.com/sagernet/sing-box/service/manager/constant"
)

var _ CM.Manager = (*Client)(nil)

type countReply struct {
	Count int `json:"count"`
}

func (s *Client) CreateSquad(in CM.SquadCreate) (CM.Squad, error) {
	return postItem[CM.Squad](s, "/squads", in)
}

func (s *Client) GetSquads(f map[string][]string) ([]CM.Squad, error) {
	return getList[CM.Squad](s, "/squads", f)
}

func (s *Client) GetSquadsCount(f map[string][]string) (int, error) {
	return getCount(s, "/squads", f)
}

func (s *Client) GetSquad(id int) (CM.Squad, error) {
	return getItem[CM.Squad](s, "/squads"+intPath(id))
}

func (s *Client) UpdateSquad(id int, in CM.SquadUpdate) (CM.Squad, error) {
	return putItem[CM.Squad](s, "/squads"+intPath(id), in)
}

func (s *Client) DeleteSquad(id int) (CM.Squad, error) {
	return deleteItem[CM.Squad](s, "/squads"+intPath(id))
}

func (s *Client) CreateNode(in CM.NodeCreate) (CM.Node, error) {
	return postItem[CM.Node](s, "/nodes", in)
}

func (s *Client) GetNodes(f map[string][]string) ([]CM.Node, error) {
	return getList[CM.Node](s, "/nodes", f)
}

func (s *Client) GetNodesCount(f map[string][]string) (int, error) {
	return getCount(s, "/nodes", f)
}

func (s *Client) GetNode(uuid string) (CM.Node, error) {
	return getItem[CM.Node](s, "/nodes"+stringPath(uuid))
}

func (s *Client) GetNodeStatus(uuid string) (string, error) {
	var reply struct {
		Status string `json:"status"`
	}
	if err := s.doJSON(http.MethodGet, "/nodes"+stringPath(uuid)+"/status", nil, nil, &reply); err != nil {
		return "", err
	}
	return reply.Status, nil
}

func (s *Client) UpdateNode(uuid string, in CM.NodeUpdate) (CM.Node, error) {
	return putItem[CM.Node](s, "/nodes"+stringPath(uuid), in)
}

func (s *Client) DeleteNode(uuid string) (CM.Node, error) {
	return deleteItem[CM.Node](s, "/nodes"+stringPath(uuid))
}

func (s *Client) CreateUser(in CM.UserCreate) (CM.User, error) {
	return postItem[CM.User](s, "/users", in)
}

func (s *Client) GetUsers(f map[string][]string) ([]CM.User, error) {
	return getList[CM.User](s, "/users", f)
}

func (s *Client) GetUsersCount(f map[string][]string) (int, error) {
	return getCount(s, "/users", f)
}

func (s *Client) GetUser(id int) (CM.User, error) {
	return getItem[CM.User](s, "/users"+intPath(id))
}

func (s *Client) UpdateUser(id int, in CM.UserUpdate) (CM.User, error) {
	return putItem[CM.User](s, "/users"+intPath(id), in)
}

func (s *Client) DeleteUser(id int) (CM.User, error) {
	return deleteItem[CM.User](s, "/users"+intPath(id))
}

func (s *Client) CreateBandwidthLimiter(in CM.BandwidthLimiterCreate) (CM.BandwidthLimiter, error) {
	return postItem[CM.BandwidthLimiter](s, "/bandwidth-limiters", in)
}

func (s *Client) GetBandwidthLimiters(f map[string][]string) ([]CM.BandwidthLimiter, error) {
	return getList[CM.BandwidthLimiter](s, "/bandwidth-limiters", f)
}

func (s *Client) GetBandwidthLimitersCount(f map[string][]string) (int, error) {
	return getCount(s, "/bandwidth-limiters", f)
}

func (s *Client) GetBandwidthLimiter(id int) (CM.BandwidthLimiter, error) {
	return getItem[CM.BandwidthLimiter](s, "/bandwidth-limiters"+intPath(id))
}

func (s *Client) UpdateBandwidthLimiter(id int, in CM.BandwidthLimiterUpdate) (CM.BandwidthLimiter, error) {
	return putItem[CM.BandwidthLimiter](s, "/bandwidth-limiters"+intPath(id), in)
}

func (s *Client) DeleteBandwidthLimiter(id int) (CM.BandwidthLimiter, error) {
	return deleteItem[CM.BandwidthLimiter](s, "/bandwidth-limiters"+intPath(id))
}

func (s *Client) CreateTrafficLimiter(in CM.TrafficLimiterCreate) (CM.TrafficLimiter, error) {
	return postItem[CM.TrafficLimiter](s, "/traffic-limiters", in)
}

func (s *Client) GetTrafficLimiters(f map[string][]string) ([]CM.TrafficLimiter, error) {
	return getList[CM.TrafficLimiter](s, "/traffic-limiters", f)
}

func (s *Client) GetTrafficLimitersCount(f map[string][]string) (int, error) {
	return getCount(s, "/traffic-limiters", f)
}

func (s *Client) GetTrafficLimiter(id int) (CM.TrafficLimiter, error) {
	return getItem[CM.TrafficLimiter](s, "/traffic-limiters"+intPath(id))
}

func (s *Client) UpdateTrafficLimiter(id int, in CM.TrafficLimiterUpdate) (CM.TrafficLimiter, error) {
	return putItem[CM.TrafficLimiter](s, "/traffic-limiters"+intPath(id), in)
}

func (s *Client) UpdateTrafficLimiterUsed(id int, used uint64) (CM.TrafficLimiter, error) {
	return putItem[CM.TrafficLimiter](s, "/traffic-limiters"+intPath(id)+"/used", struct {
		Used uint64 `json:"used"`
	}{Used: used})
}

func (s *Client) DeleteTrafficLimiter(id int) (CM.TrafficLimiter, error) {
	return deleteItem[CM.TrafficLimiter](s, "/traffic-limiters"+intPath(id))
}

func (s *Client) CreateConnectionLimiter(in CM.ConnectionLimiterCreate) (CM.ConnectionLimiter, error) {
	return postItem[CM.ConnectionLimiter](s, "/connection-limiters", in)
}

func (s *Client) GetConnectionLimiters(f map[string][]string) ([]CM.ConnectionLimiter, error) {
	return getList[CM.ConnectionLimiter](s, "/connection-limiters", f)
}

func (s *Client) GetConnectionLimitersCount(f map[string][]string) (int, error) {
	return getCount(s, "/connection-limiters", f)
}

func (s *Client) GetConnectionLimiter(id int) (CM.ConnectionLimiter, error) {
	return getItem[CM.ConnectionLimiter](s, "/connection-limiters"+intPath(id))
}

func (s *Client) UpdateConnectionLimiter(id int, in CM.ConnectionLimiterUpdate) (CM.ConnectionLimiter, error) {
	return putItem[CM.ConnectionLimiter](s, "/connection-limiters"+intPath(id), in)
}

func (s *Client) DeleteConnectionLimiter(id int) (CM.ConnectionLimiter, error) {
	return deleteItem[CM.ConnectionLimiter](s, "/connection-limiters"+intPath(id))
}

func (s *Client) CreateRateLimiter(in CM.RateLimiterCreate) (CM.RateLimiter, error) {
	return postItem[CM.RateLimiter](s, "/rate-limiters", in)
}

func (s *Client) GetRateLimiters(f map[string][]string) ([]CM.RateLimiter, error) {
	return getList[CM.RateLimiter](s, "/rate-limiters", f)
}

func (s *Client) GetRateLimitersCount(f map[string][]string) (int, error) {
	return getCount(s, "/rate-limiters", f)
}

func (s *Client) GetRateLimiter(id int) (CM.RateLimiter, error) {
	return getItem[CM.RateLimiter](s, "/rate-limiters"+intPath(id))
}

func (s *Client) UpdateRateLimiter(id int, in CM.RateLimiterUpdate) (CM.RateLimiter, error) {
	return putItem[CM.RateLimiter](s, "/rate-limiters"+intPath(id), in)
}

func (s *Client) DeleteRateLimiter(id int) (CM.RateLimiter, error) {
	return deleteItem[CM.RateLimiter](s, "/rate-limiters"+intPath(id))
}

func getList[T any](c *Client, path string, filters map[string][]string) ([]T, error) {
	var out []T
	err := c.doJSON(http.MethodGet, path, filtersQuery(filters), nil, &out)
	return out, err
}

func getCount(c *Client, path string, filters map[string][]string) (int, error) {
	var out countReply
	err := c.doJSON(http.MethodGet, path+"/count", filtersQuery(filters), nil, &out)
	return out.Count, err
}

func getItem[T any](c *Client, path string) (T, error) {
	var out T
	err := c.doJSON(http.MethodGet, path, nil, nil, &out)
	return out, err
}

func postItem[T any, In any](c *Client, path string, in In) (T, error) {
	var out T
	err := c.doJSON(http.MethodPost, path, nil, in, &out)
	return out, err
}

func putItem[T any, In any](c *Client, path string, in In) (T, error) {
	var out T
	err := c.doJSON(http.MethodPut, path, nil, in, &out)
	return out, err
}

func deleteItem[T any](c *Client, path string) (T, error) {
	var out T
	err := c.doJSON(http.MethodDelete, path, nil, nil, &out)
	return out, err
}
