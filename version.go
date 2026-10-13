package main

import (
	"fmt"
	"runtime/debug"
)

func printVersion() error {
	fmt.Println("PoH checker for CAR files")
	tag, commit := GitTag, GitCommit
	info, ok := debug.ReadBuildInfo()
	if ok {
		// A plain `go build` (as the rpcpool role does) sets no ldflags; Go stamps the tag and commit itself.
		if tag == "" {
			tag = info.Main.Version
		}
		if commit == "" {
			commit = buildSetting(info, "vcs.revision")
			if buildSetting(info, "vcs.modified") == "true" {
				commit += " (modified)"
			}
		}
	}
	fmt.Printf("Tag/Branch: %s\n", tag)
	fmt.Printf("Commit: %s\n", commit)
	if ok {
		fmt.Printf("More info:\n")
		for _, setting := range info.Settings {
			if isAnyOf(setting.Key,
				"-compiler",
				"GOARCH",
				"GOOS",
				"GOAMD64",
				"vcs",
				"vcs.revision",
				"vcs.time",
				"vcs.modified",
			) {
				fmt.Printf("  %s: %s\n", setting.Key, setting.Value)
			}
		}
	}
	return nil
}

func buildSetting(info *debug.BuildInfo, key string) string {
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

var (
	GitCommit string
	GitTag    string
)

func isAnyOf(s string, anyOf ...string) bool {
	for _, v := range anyOf {
		if s == v {
			return true
		}
	}
	return false
}
