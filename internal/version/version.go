// Package version holds the release version, stamped at build time.
package version

// Version is the release version without a leading "v". Release builds set it with
// -ldflags "-X github.com/yggy-tech/yggy-cli/internal/version.Version=1.2.3".
// A build from source keeps "dev".
var Version = "dev"
