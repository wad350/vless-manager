package main

// Build-time metadata. Injected via -ldflags by the Makefile.
var (
	Version          = "dev"
	BuildDate        = "unknown"
	BundledXray      = "unknown"
	UpdateRepository = "wad350/vless-manager"
)

// BuildInfo is exposed over /api/version.
type BuildInfo struct {
	Manager          string `json:"manager"`
	BuildDate        string `json:"build_date"`
	Xray             string `json:"xray"`
	UpdateRepository string `json:"update_repository"`
}

func buildInfo() BuildInfo {
	return BuildInfo{
		Manager:          Version,
		BuildDate:        BuildDate,
		Xray:             BundledXray,
		UpdateRepository: UpdateRepository,
	}
}
