package state

func readInstallationID() string {
	return readInstallationIDFiles("/etc/machine-id", "/var/lib/dbus/machine-id")
}
