package conf_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
)

func TestConfigBuildRejectsLegacyReverse(t *testing.T) {
	for _, raw := range []string{
		`{"reverse":{}}`,
		`{"reverse":{"bridges":[{"tag":"legacy","domain":"example.invalid"}]}}`,
		`{"reverse":{"portals":[{"tag":"legacy","domain":"example.invalid"}]}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var config conf.Config
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			built, err := config.Build()
			if built != nil || err == nil {
				t.Fatalf("legacy reverse built config %v with error %v", built, err)
			}
			if !strings.Contains(err.Error(), `"legacy reverse"`) || !strings.Contains(err.Error(), `"VLESS Reverse Proxy"`) {
				t.Fatalf("legacy reverse rejection lost migration guidance: %v", err)
			}
		})
	}
}
