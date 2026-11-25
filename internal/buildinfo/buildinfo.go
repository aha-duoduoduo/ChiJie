// Package buildinfo exposes the identity embedded in a release binary.
package buildinfo

import "runtime/debug"

// Release builds set these with -ldflags -X. Plain go builds use VCS metadata.
var Revision string
var BuiltAt string

type Info struct {
	Version  string
	Revision string
	BuiltAt  string
}

func Current() Info {
	revision, modified := Revision, false
	if revision == "" {
		if meta, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range meta.Settings {
				switch setting.Key {
				case "vcs.revision":
					revision = setting.Value
				case "vcs.modified":
					modified = setting.Value == "true"
				}
			}
		}
	}
	version := "development"
	if revision != "" {
		version = "sha-" + revision[:min(12, len(revision))]
		if modified {
			version += "-modified"
		}
	}
	return Info{Version: version, Revision: revision, BuiltAt: BuiltAt}
}
