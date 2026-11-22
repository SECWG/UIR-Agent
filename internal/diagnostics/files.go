package diagnostics

// OwnedFiles returns the diagnostic files owned by this Agent instance. Callers
// may use the returned copy for uninstall without changing rotation policy.
func OwnedFiles() []string {
	return append(append([]string{}, runtimeLogNames...), crashLogNames...)
}
