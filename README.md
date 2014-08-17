plugins
=======

Package plugins provides support for creating plugin-based applications.

Following workflow is supposed:

1. main application creates RPC Host service;
2. plugin starts and connects to the RPC Host:
	1. plugin informs RPC Host about service, provided by that plugin and requests port to serve on;
	2. RPC Host checks if such service is not provided by other connected plugin and generates port for plugin to serve on;
	3. plugin starts RPC server on port, provided by RPC Host, and informs RPC Host about it;
	4. RPC Host registers plugin service;
3. when plugin terminates, it informs RPC Host; RPC Host unregisters plugin service;
4. when RPC Host terminates, it informs all connected plugins; plugins terminates;
5. when main application need to invoke some service method, RPC Host is used for dispatch it;

For handling remote calls net/rpc package usage is supposed.
A plugin must register object(s) that will be used for handling remote calls.
