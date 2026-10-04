package buildinfo

const SurfaceVersion = "openlinker.cli.v1"

// Version is overridden by release builds through -ldflags.
var Version = "dev"

var capabilities = []string{
	"auth.login",
	"auth.logout",
	"auth.status",
	"agents.card",
	"agents.get",
	"agents.search",
	"runs.artifacts",
	"runs.async",
	"runs.cancel",
	"runs.children",
	"runs.events",
	"runs.get",
	"runs.messages",
	"runs.sync",
	"tasks.create",
}

func Capabilities() []string {
	return append([]string(nil), capabilities...)
}
