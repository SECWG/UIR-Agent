// Package terminal adapts interactive shells to Unix PTYs and Windows ConPTY or
// WinPTY. Platform-specific implementations use build constraints; embedded WinPTY
// components and their licenses stay alongside the adapter that releases them.
// The caller owns terminal lifetime and must close each opened Process.
package terminal
