package state

import (
	"crypto/sha256"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
)

var localInstallationID = sync.OnceValue(func() string { return readInstallationID() })

// This is a reinstall lookup hint, never a credential. Do not transmit the OS identifier.
func InstallationKey(server string) string {
	return installationKeyFor(localInstallationID(), server)
}

func normalizeInstallationID(machine string) string {
	machine = strings.ToLower(strings.TrimSpace(machine))
	machine = strings.ReplaceAll(machine, "-", "")
	if len(machine) != 32 || strings.Trim(machine, "0") == "" {
		return ""
	}
	for _, c := range machine {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return ""
		}
	}
	return machine
}

func installationKeyFor(machine, server string) string {
	machine = normalizeInstallationID(machine)
	if machine == "" {
		return ""
	}
	key, err := ServerKey(server)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte("UIR-Agent installation v1\x00"+runtime.GOOS+"\x00"+key+"\x00"+machine)))
}

// Linux supplies its ordered candidate paths.
func readInstallationIDFiles(paths ...string) string {
	for _, path := range paths {
		if raw, err := os.ReadFile(path); err == nil && len(raw) <= 128 && normalizeInstallationID(string(raw)) != "" {
			return string(raw)
		}
	}
	return ""
}
