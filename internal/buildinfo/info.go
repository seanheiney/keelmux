// Package buildinfo describes the exact Keelmux executable being run.
package buildinfo

import (
	"fmt"
	"runtime"
)

// Release builds set these values through Go's -X linker flags.
var (
	Version = "development"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuiltAt   string `json:"built_at"`
	Protocol  string `json:"protocol"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func Current(protocol string) Info {
	return Info{Name: "Keelmux", Version: Version, Commit: Commit, BuiltAt: BuiltAt,
		Protocol: protocol, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
}

func String(protocol string) string {
	return fmt.Sprintf("Keelmux %s (commit %s, protocol %s)", Version, Commit, protocol)
}
