package plugins_test

import (
	"github.com/kyuri/plugins"
)

func ExampleNewHost() {
	if h, err := plugins.NewHost("RPC host", &plugins.Options{tcpPort: 5000}); err == nil {
		h.Serve()
	}
}

func ExampleNewPlugin() {
	if p, err := plugins.NewPlugin("calculator", "calcService", &plugins.Options{tcpPort: 5000}); err == nil {
		p.Serve()
	}
}
