package buildinfo

// Build metadata can be overridden at build time via -ldflags.
var Version = "dev"
var Commit = "unknown"
var BuildTime = "unknown"
var BuildSource = "local"
