package constant

type ConnectedNode interface {
	UpdateUser(user User)
	UpdateUsers(users []User)
	DeleteUser(user User)

	UpdateConnectionLimiter(limiter ConnectionLimiter)
	UpdateConnectionLimiters(limiter []ConnectionLimiter)
	DeleteConnectionLimiter(limiter ConnectionLimiter)

	UpdateBandwidthLimiter(limiter BandwidthLimiter)
	UpdateBandwidthLimiters(limiter []BandwidthLimiter)
	DeleteBandwidthLimiter(limiter BandwidthLimiter)

	UpdateTrafficLimiter(limiter TrafficLimiter)
	UpdateTrafficLimiters(limiter []TrafficLimiter)
	DeleteTrafficLimiter(limiter TrafficLimiter)

	UpdateRateLimiter(limiter RateLimiter)
	UpdateRateLimiters(limiter []RateLimiter)
	DeleteRateLimiter(limiter RateLimiter)

	IsLocal() bool
	IsOnline() bool

	Close() error
}
