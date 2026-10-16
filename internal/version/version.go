// Package version 保存构建版本信息。
//
// 这些变量通过 -ldflags 在构建时注入，例如：
//
//	go build -ldflags "-X codearts2api/internal/version.Version=v1.2.3"
//
// 未注入时保留默认值，便于本地开发与 go run。
package version

import (
	"fmt"
	"runtime"
	"strings"
)

// 构建时可通过 -ldflags 覆盖。
var (
	// Version 是语义化版本号（如 v1.2.3），默认 dev。
	Version = "dev"
	// Commit 是构建时的 git 提交短哈希。
	Commit = "none"
	// Date 是构建时间（RFC3339）。
	Date = "unknown"
)

// String 返回单行版本摘要。
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s)", Version, Commit, Date, runtime.Version())
}

// Short 返回精简版本号，用于日志前缀等场景。
func Short() string {
	if Version == "" {
		return "dev"
	}
	return Version
}

// UserAgent 返回用于 HTTP 请求的 User-Agent 值。
func UserAgent() string {
	commit := Commit
	if len(commit) > 8 {
		commit = commit[:8]
	}
	parts := []string{"codearts2api", Version}
	if commit != "" && commit != "none" {
		parts = append(parts, commit)
	}
	return strings.Join(parts, "/")
}
