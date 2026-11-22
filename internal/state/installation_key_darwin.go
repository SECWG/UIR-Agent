package state

import (
	"context"
	"os/exec"
	"regexp"
	"time"
)

func readInstallationID() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	match := regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f-]{36})"`).FindSubmatch(raw)
	if len(match) != 2 {
		return ""
	}
	return string(match[1])
}
