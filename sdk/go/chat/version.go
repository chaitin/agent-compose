package chat

import (
	"runtime/debug"
	"slices"
	"sync"
)

// modulePath is this SDK's module, whose version names the client in its
// User-Agent.
const modulePath = "github.com/chaitin/agent-compose/sdk/go"

// develVersion stands in for a build that records no SDK version.
const develVersion = "devel"

// userAgent identifies this client to the daemon by the SDK version the
// program was built with, so the daemon can tell releases apart without a
// constant here having to be kept in step with the tags.
var userAgent = sync.OnceValue(func() string {
	info, _ := debug.ReadBuildInfo()
	return "agent-compose-chat-go/" + moduleVersion(info)
})

// moduleVersion reports the SDK version recorded in info: the version a
// program depending on the SDK required, or the one a replace substituted.
// A build that records none - a replace pointing at a directory, the SDK's own
// tests, a binary built without module support - reports [develVersion].
func moduleVersion(info *debug.BuildInfo) string {
	if info == nil {
		return develVersion
	}
	module := &info.Main
	if module.Path != modulePath {
		i := slices.IndexFunc(info.Deps, func(dep *debug.Module) bool { return dep.Path == modulePath })
		if i < 0 {
			return develVersion
		}
		module = info.Deps[i]
	}
	if module.Replace != nil {
		module = module.Replace
	}
	if module.Version == "" || module.Version == "(devel)" {
		return develVersion
	}
	return module.Version
}
