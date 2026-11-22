package agent

// Options contains platform connection and startup settings, not task commands.
type Options struct {
	Server, JoinToken, DataDir, Proxy string
	Recover                           bool
	DisableRelay                      bool
	Background                        bool
	// instanceID belongs to one Serve invocation, never to a persisted identity.
	instanceID string
	// OnReady runs under the existing instance lock, before accepting platform work.
	// running=false means another process already owns this device.
	OnReady func(deviceID string, running bool) error
}
