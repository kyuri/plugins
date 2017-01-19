/*
Plugin_calc is "calculator" plugin that increments given value by one.
*/
package main

import (
	"flag"
	"github.com/kyuri/plugins"
	"net/rpc"
)

// Calculator implements Calculator RPC service.
type Calculator struct{}

// Inc increments given value by one.
func (c *Calculator) Inc(in int, out *int) error {
	*out = in + 1
	return nil
}

func main() {
	rpcAddr := flag.String("RPCAddr", "", "Address for RPC communication with host application")
	flag.Parse()
	if err := rpc.Register(&Calculator{}); err == nil {
		if p, err := plugins.NewPlugin("calculator (increments by one)", "calcService", &plugins.Options{Address: *rpcAddr}); err == nil {
			defer p.Stop()
			p.Serve()
		}
	}
}
