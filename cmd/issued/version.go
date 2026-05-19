package main

// BuildSHA is injected at build time via ldflags:
//
//	-X sync.sstools.co/cmd/issued.BuildSHA=$(git rev-parse --short HEAD)
var BuildSHA = "dev"
