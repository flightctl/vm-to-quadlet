// Vendored from github.com/containers/podman (fork at ~/dev/podman).
// Import paths rewritten from go.podman.io/podman/v6 -> github.com/flightctl/vm-to-quadlet.
// No other changes.
package quadlet

import (
	"fmt"
	"path/filepath"

	"github.com/flightctl/vm-to-quadlet/internal/third_party/systemd/parser"
	"github.com/flightctl/vm-to-quadlet/internal/third_party/systemd/quadlet"
)

// scriptShells is the set of shell executables that trigger .sh script extraction.
var scriptShells = map[string]bool{
	"/bin/sh":     true,
	"sh":          true,
	"/usr/bin/sh": true,
}

// applyExec maps a container's command and args onto unit.
//
// When the combined sequence is exactly [shell, "-c", script], the script body
// is extracted into a companion GeneratedFile (.sh). The unit receives an
// Entrypoint=/bin/sh + Exec=/init.sh + Volume= bind-mount for the script.
//
// Returns the companion script file, or nil when no extraction occurred.
func applyExec(unit *parser.UnitFile, command, args []string, prefix, name, scriptDir string) *GeneratedFile {
	combined := append(command, args...) //nolint:gocritic // intentional: command is never reused after this call
	if len(combined) == 0 {
		return nil
	}

	if script, ok := isShellScript(combined); ok {
		return applyShellScript(unit, script, prefix, name, scriptDir)
	}

	if len(command) > 0 {
		unit.Set(quadlet.ContainerGroup, quadlet.KeyEntrypoint, command[0])
		execArgs := append(command[1:], args...)
		if len(execArgs) > 0 {
			unit.AddCmdline(quadlet.ContainerGroup, quadlet.KeyExec, execArgs)
		}
	} else if len(args) > 0 {
		unit.AddCmdline(quadlet.ContainerGroup, quadlet.KeyExec, args)
	}
	return nil
}

// isShellScript reports whether combined matches [shell, "-c", script].
func isShellScript(combined []string) (script string, ok bool) {
	if len(combined) != 3 {
		return "", false
	}
	if !scriptShells[combined[0]] || combined[1] != "-c" {
		return "", false
	}
	return combined[2], true
}

// applyShellScript handles the shell-script extraction path.
func applyShellScript(unit *parser.UnitFile, script, prefix, name, scriptDir string) *GeneratedFile {
	scriptName := fmt.Sprintf("%s-%s.sh", prefix, name)
	dir := scriptDir
	if dir == "" {
		dir = "<SCRIPT_DIR>"
	}
	hostPath := filepath.Join(dir, scriptName)

	unit.Set(quadlet.ContainerGroup, quadlet.KeyEntrypoint, "/bin/sh")
	unit.Set(quadlet.ContainerGroup, quadlet.KeyExec, "/init.sh")
	unit.Add(quadlet.ContainerGroup, quadlet.KeyVolume, fmt.Sprintf("%s:/init.sh:ro,z", hostPath))

	return &GeneratedFile{
		Name:    scriptName,
		Content: script,
	}
}
