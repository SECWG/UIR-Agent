// Package management serves connection-scoped file browsing, host information,
// interactive terminals, and SSH relays. The caller owns session cancellation and
// supplies shared file-read slots so blocked reads remain bounded across reconnects.
// Wait completes session cleanup after cancellation.
package management
