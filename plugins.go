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
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/rpc"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	SELFADDR     string = "127.0.0.1"
	DEFAULT_PORT int    = 4000
)

// Options for RPC client/server.
type Options struct {
	Address string      // TCP or unix socket address for serving connection or connecting to the remote server
	Log     *log.Logger // a object for logging operations
}

func (o *Options) network() string {
	if runtime.GOOS == "windows" {
		return "tcp"
	}
	return "unix"
}

func (o *Options) dialAddr() string {
	return o.Address
}

func (o *Options) dialAddrDescription() string {
	if o.network() == "tcp" {
		return fmt.Sprintf("TCP %s", o.Address)
	}
	return fmt.Sprintf("UNIX %s", o.Address)
}

func (o *Options) listenAddr() string {
	return o.Address
}

func (o *Options) listenAddrDescription() string {
	if o.network() == "tcp" {
		return fmt.Sprintf("TCP %s", o.Address)
	}
	return fmt.Sprintf("UNIX %s", o.Address)
}

func (o *Options) applyDefaults(logPrefix string) {
	if o.network() == "tcp" {
		if len(o.Address) == 0 {
			o.Address = fmt.Sprintf("%s:%d", SELFADDR, DEFAULT_PORT)
		} else if dp := strings.Index(o.Address, ":"); dp == 0 {
			o.Address = SELFADDR + o.Address
		} else if dp == len(o.Address) {
			o.Address = fmt.Sprintf("%s%d", o.Address, DEFAULT_PORT)
		} else {
			// todo: analize o.Address is IP or Port
		}
	}
	if o.Log == nil {
		o.Log = log.New(os.Stdout, fmt.Sprintf("[%s] ", logPrefix), 0)
	}
}

func prepareOptions(logPrefix string, options ...*Options) Options {
	var opt Options
	if len(options) > 0 {
		opt = *options[0]
	}
	opt.applyDefaults(logPrefix)
	return opt
}

// PluginInfo describes a plugin.
type PluginInfo struct {
	Name        string // Human-readable name of plugin
	ServiceName string // Name of service provided by plugin
	Address     string // Address plugin serves on
}

// rpcServer represents an RPC Server.
type rpcServer interface {
	Addr() net.Addr
	Serve()
	Stop(bool)
	OnServe(func())
	OnStop(func())
}

type rpcSrv struct {
	listener net.Listener
	log      *log.Logger
	running  bool
	sChan    chan os.Signal
	serve    []func()
	stop     []func()
}

func (s *rpcSrv) Addr() net.Addr {
	return s.listener.Addr()
}

func (s *rpcSrv) Serve() {
	if !s.running {
		s.running = true
		go func() {
			time.Sleep(100 * time.Millisecond) // wait for underlying "for" cycle starts
			for _, f := range s.serve {
				f()
			}
		}()

		for s.running {
			if conn, err := s.listener.Accept(); err != nil {
				if s.running { // skip error processing in case of rpcSrv stopped
					s.log.Fatal("accept error: " + err.Error()) //!!  panic???
				}
			} else {
				go rpc.ServeConn(conn)
			}
		}
	}
}

func (s *rpcSrv) Stop(doExit bool) {
	if s.running {
		s.running = false
		s.listener.Close()
		for _, f := range s.stop {
			f()
		}
	}
	if doExit {
		os.Exit(0)
	}
}

func (s *rpcSrv) OnServe(f func()) {
	s.serve = append(s.serve, f)
}

func (s *rpcSrv) OnStop(f func()) {
	s.stop = append(s.stop, f)
}

// newRPCServer creates a new rpcServer instance.
func newRPCServer(opt *Options, rpcServices ...interface{}) (rpcServer, error) {
	err := registerRPCServices(rpcServices...)
	if err == nil {
		opt.applyDefaults("RPC Server")
		var listener net.Listener
		if listener, err = getListener(opt); err == nil {
			opt.Log.Printf("Listening on %s\n", opt.listenAddrDescription())
			srv := &rpcSrv{
				listener: listener,
				log:      opt.Log,
				sChan:    make(chan os.Signal, 1),
				serve:    make([]func(), 0),
				stop:     make([]func(), 0)}
			signal.Notify(srv.sChan, os.Interrupt, os.Kill, syscall.SIGTERM)
			go func() {
				<-srv.sChan
				srv.Stop(true)
			}()
			return srv, nil
		}
	}
	return nil, err
}

func registerRPCServices(rpcServices ...interface{}) error {
	for _, rpcService := range rpcServices {
		err := rpc.Register(rpcService)
		if err != nil {
			return err
		}
	}
	return nil
}

func getListener(opt *Options) (net.Listener, error) {
	if opt.network() == "unix" && len(opt.Address) == 0 {
		addr, err := getSocketAddr()
		if err != nil {
			return nil, err
		}
		opt.Address = addr
	}
	return net.Listen(opt.network(), opt.listenAddr())
}

func getSocketAddr() (string, error) {
	tf, err := ioutil.TempFile("", "plugin")
	if err != nil {
		return "", err
	}
	path := tf.Name()

	// Close the file and remove it because it has to not exist for the domain socket.
	if err := tf.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}

	return path, nil
}

// rpcClient represents an RPC Client.
type rpcClient interface {
	Call(serviceMethod string, args interface{}, reply interface{}) error
	Close() error
}

type rpcClnt struct {
	clnt *rpc.Client
	log  *log.Logger
}

func (c *rpcClnt) Call(serviceMethod string, args interface{}, reply interface{}) error {
	return c.clnt.Call(serviceMethod, args, reply)
}

func (c *rpcClnt) Close() error {
	return c.clnt.Close()
}

// newRPCClient creates a new rpcClient instance.
func newRPCClient(name string, options ...*Options) (rpcClient, error) {
	opt := prepareOptions(fmt.Sprintf("plugin %s", name), options...)
	clnt, err := rpc.Dial(opt.network(), opt.dialAddr())
	if err == nil {
		return &rpcClnt{clnt: clnt, log: opt.Log}, nil
	}
	return nil, err

}

// PluginNotifyFunc is a callback function to receive notifications
type PluginNotifyFunc func(PluginInfo)

// A Host is a manager that plugins connects to.
// Host uses connected plugins for handling main application calls.
type Host interface {
	Addr() net.Addr
	Serve()
	Services() []string
	OnStop(func())
	Logger() *log.Logger
	OnConnectPlugin(f PluginNotifyFunc)
	OnDisconnectPlugin(f PluginNotifyFunc)
	Call(serviceName string, serviceMethod string, args interface{}, reply interface{}) error
}

// ErrServiceNotRegistered returns when trying to access non-registered service
var ErrServiceNotRegistered = errors.New("service is not registered")

// ErrServiceAlreadyRegistered throws when trying to register already registered service
var ErrServiceAlreadyRegistered = errors.New("service is already registered")

type connectedPlugin struct {
	info *PluginInfo
	clnt rpcClient
}

func (c *connectedPlugin) Call(serviceMethod string, args interface{}, reply interface{}) error {
	return c.clnt.Call(serviceMethod, args, reply)
}

type host struct {
	srv           rpcServer
	log           *log.Logger
	mutex         *sync.Mutex
	nextTcpPort   int   // free tcp port to connect
	availTcpPorts []int // network ports that are returned by disconnected plugins
	plugins       []*connectedPlugin
	stop          []func()
	onConnect     []PluginNotifyFunc
	onDisconnect  []PluginNotifyFunc
}

func (h *host) Addr() net.Addr {
	return h.srv.Addr()
}

func (h *host) Serve() {
	go h.srv.Serve()
}

func (h *host) Services() []string {
	rslt := make([]string, len(h.plugins))
	for i, cp := range h.plugins {
		rslt[i] = cp.info.ServiceName
	}
	return rslt
}

func (h *host) OnStop(f func()) {
	h.stop = append(h.stop, f)
}

func (h *host) Logger() *log.Logger {
	return h.log
}

func (h *host) OnConnectPlugin(f PluginNotifyFunc) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.onConnect = append(h.onConnect, f)
}

func (h *host) OnDisconnectPlugin(f PluginNotifyFunc) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.onDisconnect = append(h.onDisconnect, f)
}

func (h *host) Call(serviceName string, serviceMethod string, args interface{}, reply interface{}) error {
	var err error
	if i := h.indexOf(serviceName); i >= 0 {
		if err = h.plugins[i].Call(serviceMethod, args, reply); err == rpc.ErrShutdown {
			h.removePlugin(serviceName)
			err = ErrServiceNotRegistered
		}
		return err
	}
	return ErrServiceNotRegistered
}

func (h *host) getListenAddr() (string, error) {
	if h.srv.Addr().Network() == "tcp" {
		h.mutex.Lock()
		defer h.mutex.Unlock()
		var tcpPort int
		if len(h.availTcpPorts) > 0 {
			i := len(h.availTcpPorts) - 1
			tcpPort, h.availTcpPorts = h.availTcpPorts[i], h.availTcpPorts[:i]
		} else {
			tcpPort = h.nextTcpPort
			h.nextTcpPort++
		}
		return fmt.Sprintf("%s:%d", SELFADDR, tcpPort), nil
	}
	return getSocketAddr()
}

func (h *host) appendPlugin(cp *connectedPlugin) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if i := h.indexOf(cp.info.ServiceName); i < 0 {
		h.plugins = append(h.plugins, cp)
		for _, f := range h.onConnect {
			f(*cp.info)
		}
	}
}

func (h *host) removePlugin(serviceName string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if i := h.indexOf(serviceName); i >= 0 {
		cp := h.plugins[i]
		var dummy int
		cp.Call("RPCPlugin.Terminate", dummy, &dummy)
		h.log.Printf("Plugin disconnected: \"%s\"", cp.info.Name)
		for _, f := range h.onDisconnect {
			f(*cp.info)
		}
		if h.srv.Addr().Network() == "tcp" {
			h.availTcpPorts = append(h.availTcpPorts, tcpPort(cp.info.Address))
		}
		h.plugins = append(h.plugins[:i], h.plugins[i+1:]...)
	}
}

func (h *host) hostStop() {
	for len(h.plugins) > 0 {
		h.removePlugin(h.plugins[0].info.ServiceName)
	}
	for _, f := range h.stop {
		f()
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
	var port = 0
	if opt.network() == "tcp" {
		port = tcpPort(opt.Address)
	} else {
		opt.Address = ""
	}
	h := &host{
		log:           opt.Log,
		mutex:         &sync.Mutex{},
		nextTcpPort:   port + 1,
		availTcpPorts: make([]int, 0),
		plugins:       make([]*connectedPlugin, 0),
		onConnect:     make([]PluginNotifyFunc, 0),
		onDisconnect:  make([]PluginNotifyFunc, 0)}
	srv, err := newRPCServer(&opt, &RPCHost{host: h})
	if err == nil {
		h.srv = srv
		srv.OnStop(h.hostStop)
		return h, nil
	}
	opt.Log.Println(err)
	return nil, err
}

func tcpPort(address string) int {
	rslt := -1
	if i := strings.Index(address, ":"); i >= 0 {
		if port, err := strconv.ParseInt(address[i+1:], 10, 32); err == nil {
			rslt = int(port)
		}
	}
	return rslt
}

// RPCHost implements Host RPC service. Handles plugins requests.
type RPCHost struct {
	host *host
}

// GetListenAddr generates port for plugin to serve on.
func (rh *RPCHost) GetListenAddr(info *PluginInfo, listenAddr *string) error {
	err := rh.mustBeDisconnected(info)
	if err == nil {
		*listenAddr, err = rh.host.getListenAddr()
	}
	return err
}

// ConnectPlugin handles plugin connection to host.
func (rh *RPCHost) ConnectPlugin(info *PluginInfo, _ *int) error {
	err := rh.mustBeDisconnected(info)
	if err == nil {
		opt := prepareOptions(info.Name, &Options{Address: info.Address, Log: rh.host.log})
		var clnt rpcClient
		if clnt, err = newRPCClient(info.Name, &opt); err == nil {
			cp := &connectedPlugin{info: info, clnt: clnt}
			rh.host.appendPlugin(cp)
			rh.host.log.Printf("Plugin connected: \"%s\", handling \"%s\", serves at %s", cp.info.Name, cp.info.ServiceName, opt.dialAddrDescription())
			return nil
		}
	}
	return err
}

// DisconnectPlugin handles plugin disconnection from host.
func (rh *RPCHost) DisconnectPlugin(info *PluginInfo, _ *int) error {
	err := rh.mustBeConnected(info)
	if err == nil {
		rh.host.removePlugin(info.ServiceName)
	}
	return err
}

func (rh *RPCHost) mustBeDisconnected(info *PluginInfo) error {
	if rh.host.indexOf(info.ServiceName) >= 0 {
		return ErrServiceAlreadyRegistered
	}
	return nil
}

func (rh *RPCHost) mustBeConnected(info *PluginInfo) error {
	if rh.host.indexOf(info.ServiceName) < 0 {
		return ErrServiceNotRegistered
	}
	return nil
}

// Plugin is used for serving requests from main application.
type Plugin interface {
	Serve()
	Stop()
	Call(serviceMethod string, args interface{}, reply interface{}) error
	Logger() *log.Logger
	OnServe(func())
	OnStop(func())
}

type plugin struct {
	info  *PluginInfo
	log   *log.Logger
	srv   rpcServer
	clnt  rpcClient
	serve []func()
	stop  []func()
}

func (p *plugin) Serve() {
	p.srv.Serve()
}

func (p *plugin) Stop() {
	p.srv.Stop(false)
}

func (p *plugin) Call(serviceMethod string, args interface{}, reply interface{}) error {
	return p.clnt.Call(serviceMethod, args, reply)
}

func (p *plugin) Logger() *log.Logger {
	return p.log
}

func (p *plugin) OnServe(f func()) {
	p.serve = append(p.serve, f)
}

func (p *plugin) OnStop(f func()) {
	p.stop = append(p.stop, f)
}

func (p *plugin) handleStop() {
	for _, f := range p.stop {
		f()
	}
}

func (p *plugin) pluginServe() {
	var dummy int
	if err := p.Call("RPCHost.ConnectPlugin", p.info, &dummy); err != nil {
		p.log.Fatal(err)
	}
	for _, f := range p.serve {
		f()
	}
}

func (p *plugin) pluginStop() {
	var dummy int
	p.handleStop()
	if err := p.Call("RPCHost.DisconnectPlugin", p.info, &dummy); err != nil {
		p.log.Println(err)
	}
}

// NewPlugin creates a new Plugin instance.
func NewPlugin(pluginName string, serviceName string, options ...*Options) (Plugin, error) {
	opt := prepareOptions(pluginName, options...)
	clnt, err := newRPCClient(pluginName, &opt)
	if err == nil {
		opt.Log.Printf("Connected to RPC host: %s\n", opt.dialAddrDescription())
		info := &PluginInfo{Name: pluginName, ServiceName: serviceName}
		if err = clnt.Call("RPCHost.GetListenAddr", info, &info.Address); err == nil {
			p := &plugin{info: info, log: opt.Log, clnt: clnt}
			srvOpt := prepareOptions(pluginName, &Options{Address: info.Address, Log: opt.Log})
			var srv rpcServer
			if srv, err = newRPCServer(&srvOpt, &RPCPlugin{plugin: p}); err == nil {
				p.srv = srv
				srv.OnServe(p.pluginServe)
				srv.OnStop(p.pluginStop)
				return p, nil
			}
		}
		clnt.Close()
	}
	opt.Log.Println(err)
	return nil, err
}

// RPCPlugin implements Plugin RPC service. Handles  RPC Host requests.
type RPCPlugin struct {
	plugin *plugin
}

// Terminate handles RPC Host termination.
func (rp *RPCPlugin) Terminate(_ int, _ *int) error {
	rp.plugin.Stop()
	rp.plugin.log.Println("Terminating by RPC host request")
	os.Exit(0)
	return nil
}
