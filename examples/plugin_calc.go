package main

import (
	"net/rpc"
	"github.com/kyuri/plugins"
)

type Calculator struct {}

func (c *Calculator) Inc(in int, out *int) error {
	*out = in+1
	return nil
}


func main() {
	if err := rpc.Register(&Calculator{}); err == nil {
		if p, err := plugins.NewPlugin("calculator", "calcService"); err == nil {
			p.Serve()
		}
	}
}
