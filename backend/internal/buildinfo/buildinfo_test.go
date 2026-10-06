package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	stamped := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "ee73ff2abc"},
		{Key: "vcs.time", Value: "2026-10-05T17:22:19Z"},
		{Key: "vcs.modified", Value: "false"},
	}}
	dirty := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "ee73ff2abc"},
		{Key: "vcs.modified", Value: "true"},
	}}
	tests := []struct {
		name     string
		bi       *debug.BuildInfo
		ok       bool
		override string
		want     Info
	}{
		{"stamped git build", stamped, true, "", Info{Commit: "ee73ff2abc", CommitTime: "2026-10-05T17:22:19Z"}},
		{"build with uncommitted changes", dirty, true, "", Info{Commit: "ee73ff2abc", Modified: true}},
		{"ldflags override wins", stamped, true, "deadbeef", Info{Commit: "deadbeef"}},
		{"no VCS stamp (go run, or no .git)", &debug.BuildInfo{}, true, "", Info{Commit: "unknown"}},
		{"no build info at all", nil, false, "", Info{Commit: "unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fromBuildInfo(tt.bi, tt.ok, tt.override); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
