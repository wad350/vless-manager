package main

// Build-time metadata. Injected via -ldflags by the Makefile.
var (
	Version        = "dev"
	BuildDate      = "unknown"
	BundledSingBox = "unknown"
)

// BuildInfo is exposed over /api/version.
type BuildInfo struct {
	Manager   string `json:"manager"`
	BuildDate string `json:"build_date"`
	SingBox   string `json:"sing_box"`
}

func buildInfo() BuildInfo {
	return BuildInfo{
		Manager:   Version,
		BuildDate: BuildDate,
		SingBox:   BundledSingBox,
	}
}
