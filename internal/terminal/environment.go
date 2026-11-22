package terminal

import (
	"os"
	"os/exec"
	"strings"
)

func ValidSize(cols, rows int) bool { return cols >= 20 && cols <= 500 && rows >= 5 && rows <= 200 }

func Environment() []string {
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(value), "WG_IR_") {
			env = append(env, value)
		}
	}
	return append(env, "TERM=xterm-256color")
}
func Shells() []string {
	list := []string{}
	for _, name := range []string{"sh", "bash", "zsh", "powershell", "pwsh", "cmd"} {
		if _, err := exec.LookPath(name); err == nil {
			list = append(list, name)
		}
	}
	return list
}
