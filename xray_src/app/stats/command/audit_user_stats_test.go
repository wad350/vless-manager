package command_test

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/app/stats"
	. "github.com/xtls/xray-core/app/stats/command"
)

func TestGetUsersStatsIgnoresMalformedAndNonUserCounters(t *testing.T) {
	for _, name := range []string{
		">>>traffic>>>uplink",
		">>>traffic>>>downlink",
		"user>>>traffic>>>uplink",
		"user>>>traffic>>>downlink",
		"user>>>>>>traffic>>>uplink",
		"user>>>>>>traffic>>>downlink",
		"abcde>>alice@example.test>>>traffic>>>uplink",
		"inbound>>>alice@example.test>>>traffic>>>uplink",
		"outbound>>>alice@example.test>>>traffic>>>downlink",
	} {
		t.Run(name, func(t *testing.T) {
			manager, err := stats.NewManager(context.Background(), &stats.Config{})
			if err != nil {
				t.Fatal(err)
			}
			ignored, err := manager.RegisterCounter(name)
			if err != nil {
				t.Fatal(err)
			}
			ignored.Set(999)
			online, err := manager.RegisterOnlineMap("user>>>alice@example.test>>>online")
			if err != nil {
				t.Fatal(err)
			}
			online.AddIP("192.0.2.1")
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("accepted counter name caused a panic: %v", p)
				}
			}()
			response, err := NewStatsServer(manager).GetUsersStats(context.Background(), &GetUsersStatsRequest{
				IncludeTraffic: true,
				Reset_:         true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Users) != 1 || response.Users[0].Traffic.Uplink != 0 || response.Users[0].Traffic.Downlink != 0 {
				t.Fatalf("unrelated counter became user traffic: %+v", response.Users)
			}
			if ignored.Value() != 999 {
				t.Fatal("unrelated counter was reset")
			}
		})
	}
}

func TestGetUsersStatsPreservesUserTrafficAndResetContract(t *testing.T) {
	for _, email := range []string{"alice@example.test", " Alice+tag@example.test "} {
		t.Run(email, func(t *testing.T) {
			manager, err := stats.NewManager(context.Background(), &stats.Config{})
			if err != nil {
				t.Fatal(err)
			}
			online, err := manager.RegisterOnlineMap("user>>>" + email + ">>>online")
			if err != nil {
				t.Fatal(err)
			}
			online.AddIP("192.0.2.1")
			uplink, err := manager.RegisterCounter("user>>>" + email + ">>>traffic>>>uplink")
			if err != nil {
				t.Fatal(err)
			}
			downlink, err := manager.RegisterCounter("user>>>" + email + ">>>traffic>>>downlink")
			if err != nil {
				t.Fatal(err)
			}
			uplink.Set(101)
			downlink.Set(202)
			server := NewStatsServer(manager)
			for _, reset := range []bool{false, true} {
				response, err := server.GetUsersStats(context.Background(), &GetUsersStatsRequest{
					IncludeTraffic: true,
					Reset_:         reset,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(response.Users) != 1 {
					t.Fatalf("users = %v", response.Users)
				}
				user := response.Users[0]
				if user.Email != email || len(user.Ips) != 1 || user.Ips[0].Ip != "192.0.2.1" || user.Traffic.Uplink != 101 || user.Traffic.Downlink != 202 {
					t.Fatalf("user traffic changed: %+v", user)
				}
			}
			if uplink.Value() != 0 || downlink.Value() != 0 {
				t.Fatal("user traffic was not reset")
			}
			response, err := server.GetUsersStats(context.Background(), &GetUsersStatsRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Users) != 1 || response.Users[0].Traffic != nil {
				t.Fatal("traffic included without IncludeTraffic")
			}
		})
	}
}
