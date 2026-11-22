package execution

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/terminal"
)

func validRun(r protocol.Run) error {
	if r.Timeout < 1 || r.Timeout > 3600000 {
		return errors.New("timeout outside 1..3600000 ms")
	}
	if r.Command.Kind != "exec" && r.Command.Kind != "script" && r.Command.Kind != "collect" && r.Command.Kind != "deploy" {
		return errors.New("unsupported command kind")
	}
	if r.Command.Kind == "deploy" {
		if r.Cwd != "" ||
			len(r.Env) != 0 ||
			r.Command.Program != "" ||
			r.Command.Shell != "" ||
			r.Command.Content != "" ||
			len(r.Command.Args) != 0 ||
			len(r.Command.Paths) != 0 {
			return errors.New("invalid deployment fields")
		}
		return validDeploy(r.Command)
	}
	if r.Command.ToolID != "" || r.Command.Filename != "" || r.Command.Size != 0 || r.Command.SHA256 != "" || r.Command.Directory != "" {
		return errors.New("unexpected tool fields")
	}
	if r.Command.Kind == "exec" && (r.Command.Program == "" || strings.ContainsRune(r.Command.Program, 0)) {
		return errors.New("invalid executable")
	}
	if r.Command.Kind == "script" {
		ok := false
		for _, s := range terminal.Shells() {
			if s == r.Command.Shell {
				ok = true
			}
		}
		if !ok {
			return errors.New("shell unavailable")
		}
	}
	if r.Command.Kind == "collect" {
		if len(r.Command.Paths) < 1 ||
			len(r.Command.Paths) > 16 ||
			r.Cwd != "" ||
			len(r.Env) != 0 ||
			r.Command.Program != "" ||
			r.Command.Shell != "" ||
			r.Command.Content != "" ||
			len(r.Command.Args) != 0 {
			return errors.New("invalid file collection request")
		}
		seen := map[string]bool{}
		for _, p := range r.Command.Paths {
			if !filepath.IsAbs(p) ||
				len(p) > 4096 ||
				strings.ContainsAny(p, "\x00*?\r\n") ||
				strings.HasPrefix(p, `\\`) ||
				strings.HasPrefix(p, "//") ||
				strings.Contains(strings.TrimPrefix(p, filepath.VolumeName(p)), ":") ||
				seen[p] {
				return errors.New("collection requires unique absolute local file paths")
			}
			seen[p] = true
		}
	} else if len(r.Command.Paths) != 0 {
		return errors.New("file paths require collect command")
	}
	for k, v := range r.Env {
		if strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) || strings.HasPrefix(strings.ToUpper(k), "WG_IR_") {
			return errors.New("invalid task environment")
		}
	}
	return nil
}
