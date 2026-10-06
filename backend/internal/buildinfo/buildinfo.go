// Package buildinfo reports which commit the running binary was built from, so /health can show what's deployed.
package buildinfo

import (
	"runtime/debug"
	"sync"
)

// Commit overrides the stamped revision for builds without a .git directory:
// go build -ldflags "-X ai-chat/internal/buildinfo.Commit=$(git rev-parse HEAD)".
var Commit string

// Info is the build identity /health exposes.
type Info struct {
	Commit     string `json:"commit"`
	CommitTime string `json:"commit_time,omitempty"`
	// Modified is true when the build had uncommitted changes, i.e. it isn't exactly Commit.
	Modified bool `json:"modified,omitempty"`
}

var get = sync.OnceValue(func() Info {
	bi, ok := debug.ReadBuildInfo()
	return fromBuildInfo(bi, ok, Commit)
})

// Get returns this binary's build identity, read once.
func Get() Info { return get() }

// fromBuildInfo prefers an explicit override, then the VCS stamp `go build` embeds in a git checkout, else "unknown".
func fromBuildInfo(bi *debug.BuildInfo, ok bool, override string) Info {
	info := Info{Commit: "unknown"}
	if ok && bi != nil {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Commit = s.Value
			case "vcs.time":
				info.CommitTime = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	if override != "" {
		return Info{Commit: override}
	}
	return info
}
