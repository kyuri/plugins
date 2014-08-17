package main

import (
	"fmt"
	"time"
	"github.com/kyuri/plugins"
)

func main() {
	var arg int = 1
	var rslt int
	if h, err := plugins.NewHost("RPC host"); err == nil {
		h.Serve()
		for {
			time.Sleep(time.Second)
			if err = h.Call("calcService", "Calculator.Inc", arg, &rslt); err == nil {
				fmt.Printf("Calculator.Inc(%d) == %d\n", arg, rslt);
				arg = rslt
			} else if err != plugins.ErrServiceNotRegistered {
				fmt.Println(err.Error())
			}

		}
	}
}
