// Package protocol defines control messages, bounded command decoding, and the
// asynchronous control sender shared by the agent and execution packages.
// Wire field names and command limits are coordinated with protocol.schema.json.
// It does not perform enrollment, execute commands, or persist device state.
package protocol
