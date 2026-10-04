package buildinfo

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
)

//go:embed release.json
var files embed.FS

// Commit and BuiltAt are populated by the release build, never read from user data.
var Commit = "development"
var BuiltAt = "unknown"

type releaseConfig struct {
	Version         string `json:"version"`
	PackageRevision int    `json:"packageRevision"`
}

var release = func() releaseConfig {
	raw, err := files.ReadFile("release.json")
	if err != nil {
		panic(err)
	}
	var value releaseConfig
	if json.Unmarshal(raw, &value) != nil || !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).MatchString(value.Version) || value.PackageRevision < 1 {
		panic("invalid release.json")
	}
	return value
}()

func Version() string         { return release.Version }
func Tag() string             { return "v" + Version() }
func PackageVersion() string  { return fmt.Sprintf("%s-%d", Version(), release.PackageRevision) }
func PackageFilename() string { return "RemoteGate-" + PackageVersion() + "-istore.run" }

type Info struct {
	Version        string `json:"version"`
	PackageVersion string `json:"packageVersion"`
	Commit         string `json:"commit"`
	BuiltAt        string `json:"builtAt"`
	ReleaseURL     string `json:"releaseURL"`
	Image          string `json:"image"`
}

func Current() Info {
	return Info{Version(), PackageVersion(), Commit, BuiltAt, "https://github.com/yehao9589/remotegate/releases/tag/" + Tag(), "ghcr.io/yehao9589/remotegate:" + Tag()}
}
