package standalone

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/flightctl/vm-to-quadlet/pkg/quadlet"
)

//go:embed passt-workaround-hook.sh
var passtWorkaroundHook string

// ApplyPostConvertFixups is step 7: injects the passt workaround libvirt hook
// when opts.PasstWorkarounds is enabled. vmName is used to derive the hook
// script filename and the compute container unit name.
func ApplyPostConvertFixups(files []quadlet.UnitFile, vmName string, opts quadlet.Options) ([]quadlet.UnitFile, error) {
	if !opts.PasstWorkarounds {
		return files, nil
	}

	hookName := vmName + "-libvirt-hook.sh"
	hookPath := opts.ScriptDir + "/" + hookName
	files = injectPasstWorkaroundHook(files, vmName, hookPath)
	files = append(files, quadlet.UnitFile{
		Name:    hookName,
		Content: passtWorkaroundHook,
	})

	return files, nil
}

// injectPasstWorkaroundHook finds the compute .container file and:
//   - bind-mounts the workaround hook script over /etc/libvirt/hooks/qemu
//   - disables SELinux confinement so SCM_RIGHTS fd-passing between QEMU and passt
//     is not blocked by the host MAC policy (required by the hook's XML manipulation)
func injectPasstWorkaroundHook(files []quadlet.UnitFile, vmName, hookPath string) []quadlet.UnitFile {
	computeName := vmName + "-compute.container"
	for i, f := range files {
		if f.Name != computeName {
			continue
		}
		extra := fmt.Sprintf("Volume=%s:/etc/libvirt/hooks/qemu:z,exec\n", hookPath) +
			"SecurityLabelDisable=true\n"
		files[i].Content = insertBeforeSection(f.Content, "Service", extra)
		return files
	}
	return files
}

// insertBeforeSection inserts extra lines immediately before the named INI
// section header. Falls back to appending at the end if the section is not found.
func insertBeforeSection(content, section, extra string) string {
	marker := "\n[" + section + "]"
	idx := strings.Index(content, marker)
	if idx == -1 {
		return content + "\n" + extra
	}
	return content[:idx] + "\n" + extra + content[idx:]
}
