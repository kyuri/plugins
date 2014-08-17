/*
	Package plugins provides support for creating plugin-based applications.

	Following workflow is supposed:
		1) main application creates RPC Host service;
		2) plugin starts and connects to the RPC Host:
			2.1) plugin informs RPC Host about service, provided by that plugin and requests port to serve on;
	  		2.2) RPC Host checks if such service is not provided by other connected plugin and generates port for plugin to serve on;
			2.3) plugin starts RPC server on port, provided by RPC Host, and informs RPC Host about it;
			2.4) RPC Host registers plugin service;
		3) when plugin terminates, it informs RPC Host; RPC Host unregisters plugin service;
		4) when RPC Host terminates, it informs all connected plugins; plugins terminates;
		5) when main application need to invoke some service method, RPC Host is used for dispatch it;

	For handling remote calls net/rpc package usage is supposed.
	A plugin must register object(s) that will be used for handling remote calls.

*/
package plugins

import (
	"fmt"
	"log"
	"time"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"net"
	"net/rpc"
)

// Options for RPC client/server.
type Options struct {
	ipAddr	string
	Port	int				// network port for serving connection or connecting to the remote server
	Log		*log.Logger		// a object for logging operations
}

func (o *Options) listenAddr() string {
	return fmt.Sprintf(":%d", o.Port)
}

func (o *Options) dialAddr() string {
	return fmt.Sprintf("%s:%d", o.ipAddr, o.Port)
}

func (o *Options) applyDefaults(name string) {
	if len(o.ipAddr) == 0 {
		o.ipAddr = "127.0.0.1"
	}
	if o.Port <= 0 {
		o.Port = 4000
	}
	if o.Log == nil {
		o.Log = log.New(os.Stdout, fmt.Sprintf("[%s] ", name), 0)
	}
}

func prepareOptions(name string, options ...*Options) Options {
	var opt Options
	if len(options) > 0 {
		opt = *options[0]
	}
	opt.applyDefaults(name)
	return opt
}

// PluginInfo describes a plugin.
type PluginInfo struct {
	Name			string		// Human-readable name of plugin
	ServiceName		string		// Name of service provided by plugin
	Port			int			// Port plugin serves on
}

// rpcServer represents an RPC Server.
type rpcServer interface {
	Serve()
	onServe(func())
	onStop(func())
}

type rpcSrv struct {
	listener	net.Listener
	log			*log.Logger
	running		bool
	sChan		chan os.Signal
	serve		[]func()
	stop		[]func()
}

func (s *rpcSrv) Serve() {
	if !s.running {
		s.running = true
		go func() {
			time.Sleep(500 * time.Millisecond)
			for _, f := range s.serve {
				f()
			}
		}()

		for {
			if conn, err := s.listener.Accept(); err != nil {
				s.log.Fatal("accept error: " + err.Error())         //!!  panic???
			} else {
				go rpc.ServeConn(conn)
			}
		}
	}
};

func (s *rpcSrv) onServe(h func()) {
	s.serve = append(s.serve, h)
}

func (s *rpcSrv) onStop(h func()) {
	s.stop = append(s.stop, h)
}

func (s *rpcSrv) handleStop() {
	for _, f := range s.stop {
		f()
	}
	os.Exit(0);
}

// newRpcServer creates a new rpcServer instance.
func newRpcServer(opt *Options, rpcServices ...interface{}) (rpcServer, error) {
	err := registerRpcServices(rpcServices...)
	if err == nil {
		opt.applyDefaults("RPC Server")
		listener, err := net.Listen("tcp", opt.listenAddr())
		if err == nil {
			opt.Log.Printf("Listening on %s\n", opt.listenAddr())
			srv := &rpcSrv {
				listener: listener,
				log: opt.Log,
				sChan: make(chan os.Signal, 1),
				serve: make([]func(), 0),
				stop: make([]func(), 0)}
			signal.Notify(srv.sChan, os.Interrupt, os.Kill, syscall.SIGTERM)
			go func() {
				<- srv.sChan
				if srv.running {
					srv.handleStop()
				}
			}()
			return srv, nil
		}
	}
	return nil, err;
}

func registerRpcServices(rpcServices ...interface{}) error {
	for _, rpcService := range rpcServices {
		err := rpc.Register(rpcService)
		if err != nil {
			return err
		}
	}
	return nil
}

// rpcClient represents an RPC Client.
type rpcClient interface {
	Call(serviceMethod string, args interface{}, reply interface{}) error
	Close() error
}

type rpcClnt struct {
	clnt	*rpc.Client
	log		*log.Logger
}

func (c *rpcClnt) Call(serviceMethod string, args interface{}, reply interface{}) error {
	return c.clnt.Call(serviceMethod, args, reply)
}

func (c *rpcClnt) Close() error {
	return c.clnt.Close()
}

// newRpcClient creates a new rpcClient instance.
func newRpcClient(name string, options ...*Options) (rpcClient, error) {
	opt := prepareOptions(fmt.Sprintf("plugin %s", name), options...)
	if clnt, err := rpc.Dial("tcp", opt.dialAddr()); err != nil {
		return nil, err;
	} else {
		clnt := &rpcClnt{clnt: clnt, log: opt.Log}
		return clnt, nil
	}
}

// A Host is a manager that plugins connects to.
// Host uses connected plugins for handling main application calls.
type Host interface {
	Serve()
	Call(serviceName string, serviceMethod string, args interface{}, reply interface{}) error
}

var ErrServiceNotRegistered = errors.New("Service is not registered")
var ErrServiceAlreadyRegistered = errors.New("Service is already registered")

type connectedPlugin struct {
	info	*PluginInfo
	clnt	rpcClient
}

func (c *connectedPlugin) Call(serviceMethod string, args interface{}, reply interface{}) error {
	return c.clnt.Call(serviceMethod, args, reply)
}

type host struct {
	srv			rpcServer
	log			*log.Logger
	nextPort	int					// free network port to connect
	availPorts	[]int				// network ports that are returned by disconnected plugins
	plugins		[]*connectedPlugin
}

func (h *host) Serve() {
	go h.srv.Serve()
}

func (h *host) Call(serviceName string, serviceMethod string, args interface{}, reply interface{}) error {
	var err error
	if i := h.indexOf(serviceName); i>=0 {
		if err = h.plugins[i].Call(serviceMethod, args, reply); err == rpc.ErrShutdown {
			h.removePlugin(serviceName)
			err = ErrServiceNotRegistered
		}
		return err
	}
	return ErrServiceNotRegistered
}

func (h *host) getNextPort() int {
	var rslt int
	if len(h.availPorts) > 0 {
		i := len(h.availPorts) - 1
		rslt, h.availPorts = h.availPorts[i], h.availPorts[:i]
	} else {
		rslt = h.nextPort
		h.nextPort += 1
	}
	return rslt
}

func (h *host) appendPlugin(cp *connectedPlugin) {
	if i := h.indexOf(cp.info.ServiceName); i<0 {
		h.plugins = append(h.plugins, cp)
	}
}

func (h *host) removePlugin(serviceName string) {
	if i := h.indexOf(serviceName); i>=0 {
		cp := h.plugins[i]
		var dummy int
		cp.Call("RpcPlugin.Terminate", dummy, &dummy)
		h.log.Printf("Plugin disconnected: \"%s\"", cp.info.Name)
		h.availPorts = append(h.availPorts, cp.info.Port)
		h.plugins = append(h.plugins[:i], h.plugins[i+1:]...)
	}
}

func (h *host) onStop() {
	for len(h.plugins)>0 {
		h.removePlugin(h.plugins[0].info.ServiceName)
	}
}

func (h *host) indexOf(serviceName string) int {
	for i, cp := range h.plugins {
		if cp.info.ServiceName == serviceName {
			return i
		}
	}
	return -1
}

// NewHost creates a new Host instance.
func NewHost(name string, options ...*Options) (Host, error) {
	opt := prepareOptions(name, options...)
	h := &host{
		log: opt.Log,
		nextPort: opt.Port+1,
		availPorts: make([]int, 0),
		plugins: make([]*connectedPlugin, 0)}
	rpcHostService := &RpcHost{host: h}
	if srv, err := newRpcServer(&opt, rpcHostService); err == nil {
		h.srv = srv
		srv.onStop(h.onStop)
		return h, nil
	} else {
		opt.Log.Println(err)
		return nil, err
	}
}

// Host RPC service. Handles plugins requests.
type RpcHost struct {
	host *host
}

// Generates port for plugin to serve on.
func (rh *RpcHost) GetPort(info *PluginInfo, port *int) error {
	err := rh.mustBeDisconnected(info)
	if err == nil {
		*port = rh.host.getNextPort()
	}
	return err
}

// Handles plugin connection to host.
func (rh *RpcHost) ConnectPlugin(info *PluginInfo, _ *int) error {
	err := rh.mustBeDisconnected(info)
	if err == nil {
		opt := prepareOptions(info.Name, &Options{Port: info.Port, Log: rh.host.log})
		if clnt, err := newRpcClient(info.Name, &opt); err == nil {
			cp := &connectedPlugin{info: info, clnt: clnt}
			rh.host.appendPlugin(cp)
			rh.host.log.Printf("Plugin connected: \"%s\", handling \"%s\", serves at %s", cp.info.Name, cp.info.ServiceName, opt.dialAddr())
			return nil
		}
	}
	return err
}

// Handles plugin disconnection from host.
func (rh *RpcHost) DisconnectPlugin(info *PluginInfo, _ *int) error {
	err := rh.mustBeConnected(info)
	if err == nil {
		rh.host.removePlugin(info.ServiceName)
	}
	return err
}

func (rh *RpcHost) mustBeDisconnected(info *PluginInfo) error {
	if rh.host.indexOf(info.ServiceName)>=0 {
		return ErrServiceAlreadyRegistered
	}
	return nil
}

func (rh *RpcHost) mustBeConnected(info *PluginInfo) error {
	if rh.host.indexOf(info.ServiceName)<0 {
		return ErrServiceNotRegistered
	}
	return nil
}

// Plugin is used for serving requests from main application.
type Plugin interface {
	Serve()
}

type plugin struct {
	info	*PluginInfo
	log		*log.Logger
	srv		rpcServer
	clnt	rpcClient
}

func (p *plugin) Serve() {
	p.srv.Serve()
}

func (p *plugin) onServe() {
	var dummy int
	if err := p.clnt.Call("RpcHost.ConnectPlugin", p.info, &dummy); err != nil {
		p.log.Fatal(err)
	}
}

func (p *plugin) onStop() {
	var dummy int
	if err := p.clnt.Call("RpcHost.DisconnectPlugin", p.info, &dummy); err != nil {
		p.log.Println(err)
	}
}

// NewPlugin creates a new Plugin instance.
func NewPlugin(pluginName string, serviceName string, options ...*Options) (Plugin, error) {
	opt := prepareOptions(pluginName, options...)
	clnt, err := newRpcClient(pluginName, &opt)
	if err == nil {
		opt.Log.Printf("Connected to RPC host: %s\n", opt.dialAddr())
		info := &PluginInfo{Name: pluginName, ServiceName: serviceName}
		if err = clnt.Call("RpcHost.GetPort", info, &info.Port); err == nil {
			p := &plugin{info: info, log: opt.Log, clnt: clnt}
			rpcPluginService := &RpcPlugin{plugin: p}
			srvOpt := prepareOptions(pluginName, &Options{Port: info.Port, Log: opt.Log})
			if srv, err := newRpcServer(&srvOpt, rpcPluginService); err == nil {
				p.srv = srv
				srv.onServe(p.onServe)
				srv.onStop(p.onStop)
				return p, nil
			}
		}
		clnt.Close()
	}
	opt.Log.Println(err)
	return nil, err
}

// Plugin RPC service. Handles  RPC Host requests.
type RpcPlugin struct {
	plugin	*plugin
}

// Handles RPC Host termination.
func (rp *RpcPlugin) Terminate(_ int, _ *int) error {
	rp.plugin.log.Println("Terminating by RPC host request")
	os.Exit(0)
	return nil
}
