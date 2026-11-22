// Package state owns device identity, installation matching, private directories,
// and durable task records. An exclusive store lock prevents concurrent instances
// from mutating the same task cache. Its files and serialized formats must remain
// stable across restarts and source reorganizations.
package state
