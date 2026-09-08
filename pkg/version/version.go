package version

import (
	"fmt"
	"runtime"
)

// These variables are set at build time via -ldflags -X.
var (
	version      = "unknown"
	commit       = "unknown"
	buildDate    = "unknown"
	gitTreeState = "unknown"
)

// Info holds the version metadata for the binary.
type Info struct {
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	BuildDate    string `json:"buildDate"`
	GitTreeState string `json:"gitTreeState"`
	GoVersion    string `json:"goVersion"`
	Platform     string `json:"platform"`
}

// Get returns the version information populated at build time.
func Get() Info {
	return Info{
		Version:      version,
		Commit:       commit,
		BuildDate:    buildDate,
		GitTreeState: gitTreeState,
		GoVersion:    runtime.Version(),
		Platform:     runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String returns a human-readable version string.
func (i Info) String() string {
	return fmt.Sprintf("vm-to-quadlet version %s (commit %s, built %s, %s)",
		i.Version, i.Commit, i.BuildDate, i.GitTreeState)
}
