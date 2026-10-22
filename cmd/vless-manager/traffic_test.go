package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestInternetTrafficCountsWANOnly(t *testing.T) {
	routes := "Iface Destination Gateway Flags RefCnt Use Metric Mask\n" +
		"br0 00C9A8C0 00000000 0001 0 0 0 00FFFFFF\n" +
		"qmi_br0 00000000 0100000A 0003 0 0 0 00000000\n" +
		"qmi_br0 00000000 0100000A 0003 0 0 100 00000000\n"
	values := map[string]uint64{
		"/sys/class/net/qmi_br0/statistics/rx_bytes": 5_654_800_345,
		"/sys/class/net/qmi_br0/statistics/tx_bytes": 100,
	}
	var reads int
	got, err := internetTrafficSnapshot(strings.NewReader(routes), func(name string) bool {
		return name == "qmi_br0"
	}, func(path string) (uint64, error) {
		reads++
		value, ok := values[path]
		if !ok {
			t.Fatalf("unexpected counter read (LAN or modem underlay): %s", path)
		}
		return value, nil
	})
	if err != nil || got.Download != 5_654_800_345 || got.Upload != 100 || got.Interface != "qmi_br0" ||
		got.Source != "wan:qmi_br0" || reads != 2 {
		t.Fatalf("WAN snapshot = %+v, reads=%d, err=%v", got, reads, err)
	}
}

func TestInternetTrafficMultipleUplinksHaveStableSource(t *testing.T) {
	routes := "usb0 00000000 0100000A 0003 0 0 20 00000000\n" +
		"eth0 00000000 0100000A 0003 0 0 10 00000000\n"
	got, err := internetTrafficSnapshot(strings.NewReader(routes), func(string) bool { return true },
		func(path string) (uint64, error) {
			if strings.HasSuffix(path, "rx_bytes") {
				return 900, nil
			}
			return 100, nil
		})
	if err != nil || got.Download != 1800 || got.Upload != 200 || got.Interface != "eth0, usb0" ||
		got.Source != "wan:eth0,usb0" {
		t.Fatalf("multiple uplinks = %+v, err=%v", got, err)
	}
}

func TestInternetTrafficRejectsInvalidRoutes(t *testing.T) {
	for name, route := range map[string]string{
		"loopback":       "lo 00000000 00000000 0001 0 0 0 00000000",
		"managed tunnel": "tun0 00000000 00000000 0001 0 0 0 00000000",
		"route down":     "eth0 00000000 00000000 0000 0 0 0 00000000",
		"rejected":       "eth0 00000000 00000000 0201 0 0 0 00000000",
		"not default":    "eth0 0000000A 00000000 0001 0 0 0 00000000",
		"nonzero mask":   "eth0 00000000 00000000 0001 0 0 0 00FFFFFF",
		"invalid dest":   "eth0 invalid 00000000 0001 0 0 0 00000000",
		"invalid flags":  "eth0 00000000 00000000 nope 0 0 0 00000000",
		"invalid mask":   "eth0 00000000 00000000 0001 0 0 0 invalid",
		"short":          "eth0 00000000",
		"interface down": "down0 00000000 00000000 0001 0 0 0 00000000",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := internetTrafficSnapshot(strings.NewReader(route), func(iface string) bool { return iface != "down0" },
				func(string) (uint64, error) { t.Fatal("invalid route read a counter"); return 0, nil })
			if err == nil {
				t.Fatal("invalid route was accepted")
			}
		})
	}
}

func TestInternetTrafficCounterReadFailureIsNotPartialSuccess(t *testing.T) {
	unavailable := errors.New("interface disappeared")
	got, err := internetTrafficSnapshot(strings.NewReader("eth0 00000000 00000000 0001 0 0 0 00000000"),
		func(string) bool { return true }, func(path string) (uint64, error) {
			if strings.HasSuffix(path, "rx_bytes") {
				return 100, nil
			}
			return 0, unavailable
		})
	if !errors.Is(err, unavailable) || got != (internetTraffic{}) {
		t.Fatalf("partial snapshot = %+v, err=%v", got, err)
	}
}

func TestInternetTrafficRouteReadFailure(t *testing.T) {
	_, err := internetTrafficSnapshot(&failedTrafficReader{}, func(string) bool { return true },
		func(string) (uint64, error) { t.Fatal("failed route read a counter"); return 0, nil })
	if err == nil {
		t.Fatal("route read failure ignored")
	}
}

type failedTrafficReader struct{}

func (*failedTrafficReader) Read([]byte) (int, error) { return 0, errors.New("route read failed") }

func TestTrafficAPIDoesNotAddSpeedtestPayloadToWAN(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.trafficRead = func() (internetTraffic, error) {
		return internetTraffic{Interface: "usb0", Source: "wan:usb0", Download: 1000, Upload: 200}, nil
	}
	api.speedTest.recordProgress(speedTestStatus{DownloadBytes: 800, UploadBytes: 50})
	rec := apiRequest(t, api, http.MethodGet, "/api/traffic", "")
	var response struct {
		Interface string
		Download  uint64 `json:"download_bytes"`
		Upload    uint64 `json:"upload_bytes"`
		Modes     map[string]struct {
			Available bool
			Source    string
			Download  uint64 `json:"download_bytes"`
			Upload    uint64 `json:"upload_bytes"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	all := response.Modes["all"]
	if rec.Code != http.StatusOK || response.Interface != "usb0" || response.Download != 1000 ||
		response.Upload != 200 || all.Download != 1000 || all.Upload != 200 ||
		!all.Available || all.Source != "wan:usb0" {
		t.Fatalf("traffic response = %s", rec.Body.String())
	}
}

func TestTrafficAPIReportsMissingWANAsUnavailable(t *testing.T) {
	api := newHandlerTestAPI(t)
	api.trafficRead = func() (internetTraffic, error) { return internetTraffic{}, errors.New("no WAN") }
	rec := apiRequest(t, api, http.MethodGet, "/api/traffic", "")
	var response struct {
		Modes map[string]struct {
			Available bool
			Download  uint64 `json:"download_bytes"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || response.Modes["all"].Available || response.Modes["all"].Download != 0 {
		t.Fatalf("missing WAN is misleading: %s", rec.Body.String())
	}
}
