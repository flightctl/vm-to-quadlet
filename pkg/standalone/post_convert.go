package standalone

import (
	"fmt"
	"strings"

	"github.com/flightctl/vm-to-quadlet/pkg/quadlet"
)

// ApplyPostConvertFixups is step 7: post-quadlet fixups applied directly to the
// generated INI text. Currently handles:
//   - PublishPort injection into the pod unit when a VNC or serial proxy is enabled.
//   - Passt binary patch: PATH override for the compute container when --passt-workarounds.
func ApplyPostConvertFixups(files []quadlet.UnitFile, vmName string, standaloneOpts Options, convOpts quadlet.Options) ([]quadlet.UnitFile, error) {
	files = injectPodPublishPorts(files, vmName, standaloneOpts)

	if convOpts.PasstWorkarounds {
		files = injectPasstBinaryPath(files, vmName)
	}

	return files, nil
}

// injectPasstBinaryPath prepends /passt-bin to PATH in the compute container so
// that libvirt's virFindFileInPath("passt") picks up the wrapper written by the
// passt-binary-patcher init container before the unpatched /usr/bin/passt.
// The wrapper calls /passt-bin/passt.avx2.patched (the patched binary in the
// shared emptyDir volume).
func injectPasstBinaryPath(files []quadlet.UnitFile, vmName string) []quadlet.UnitFile {
	computeName := vmName + "-compute.container"
	extra := "Environment=PATH=/passt-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n"
	for i, f := range files {
		if f.Name != computeName {
			continue
		}
		files[i].Content = insertBeforeSection(f.Content, "Service", extra)
		return files
	}
	return files
}

// injectPodPublishPorts stamps PublishPort= lines into the generated .pod unit
// for each proxy port that was requested. Quadlet requires these on the pod,
// not on the individual container.
func injectPodPublishPorts(files []quadlet.UnitFile, vmName string, opts Options) []quadlet.UnitFile {
	var ports []string
	if opts.AddVNCProxy {
		ports = append(ports, fmt.Sprintf("%d:%d", opts.VNCPort, opts.VNCPort))
	}
	if opts.AddSerialProxy {
		ports = append(ports, fmt.Sprintf("%d:%d", opts.SerialPort, opts.SerialPort))
	}
	if len(ports) == 0 {
		return files
	}

	podName := vmName + ".pod"
	extra := ""
	for _, p := range ports {
		extra += fmt.Sprintf("PublishPort=%s\n", p)
	}

	for i, f := range files {
		if f.Name != podName {
			continue
		}
		files[i].Content = insertBeforeSection(f.Content, "Install", extra)
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
