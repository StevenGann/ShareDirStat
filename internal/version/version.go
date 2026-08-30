// Package version holds build metadata injected at link time via -ldflags.
package version

import (
	"fmt"
	"runtime"
)

// Set by the linker: -X github.com/StevenGann/ShareDirStat/internal/version.Version=1.2.3 …
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info is the JSON shape served by GET /api/v1/version.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Go        string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Get returns the current build information.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: Date,
		Go:        runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// String renders a one-line human readable version.
func String() string {
	return fmt.Sprintf("sharedirstat %s (%s, %s, %s/%s)", Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
}
