// Package agent coordinates platform enrollment and the endpoint lifecycle.
//
// Startup binds a persistent identity to one exclusive task store. Serve owns an
// execution.Engine across control reconnects; losing a connection does not restart
// or cancel a command. Each connection owns its management sessions, which close
// when that connection ends. Shutdown and uninstall coordinate these lifetimes
// before releasing the store and device identity.
//
// Protocol types, persistence, execution, terminal adapters, and diagnostics live
// in their respective internal packages. None depends on this orchestration layer.
package agent
