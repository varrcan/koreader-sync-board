// Package buildinfo хранит версию и коммит сборки, заданные через -ldflags:
//
//	-X github.com/varrcan/koreader-sync-board/server/internal/buildinfo.Version=v0.1.0
//	-X github.com/varrcan/koreader-sync-board/server/internal/buildinfo.Commit=abc1234
package buildinfo

import "runtime/debug"

var (
	Version = "dev"
	Commit  = ""
)

// CommitOrVCS возвращает Commit, а если он не задан — ревизию VCS из сборки Go.
func CommitOrVCS() string {
	if Commit != "" {
		return Commit
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "unknown"
}
