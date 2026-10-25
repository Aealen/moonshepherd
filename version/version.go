// Package version 内嵌版本信息（Makefile 构建时注入，默认 dev）。
package version

// Version 语义化版本；构建命令：
//
//	go build -ldflags "-X github.com/maxonphong/herd/version.Version=0.1.0"
var Version = "0.1.0-dev"
