package main

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/yaml"
	virtv1 "kubevirt.io/api/core/v1"

	"github.com/flightctl/vm-to-quadlet/pkg/kubevirt"
	"github.com/flightctl/vm-to-quadlet/pkg/quadlet"
	"github.com/flightctl/vm-to-quadlet/pkg/standalone"
)

func main() {
	var (
		vmFile           string
		launcherImage    string
		vncProxy         bool
		vncPort          int
		vncImage         string
		serialProxy      bool
		consolePort      int
		serialImage      string
		outputDir        string
		passtWorkarounds bool
		passtDebug       bool
		network          string
	)

	rootCmd := &cobra.Command{
		Use:   "vm-to-quadlet [vm-file]",
		Short: "Generate native Quadlet unit files from a KubeVirt VirtualMachine YAML",
		Long: `Generate native Quadlet .container and .volume unit files from a KubeVirt VirtualMachine YAML.

The VM file can be provided as a positional argument, via --vm-file, or piped through stdin:

  kubevirt-vm-to-quadlet vm.yaml
  kubevirt-vm-to-quadlet --vm-file=vm.yaml --output-dir=./quadlet
  cat vm.yaml | kubevirt-vm-to-quadlet

Output is written to --output-dir when provided. Otherwise all files are printed to
stdout separated by "### <filename>" header lines.

The generated files should be placed in ~/.config/containers/systemd/ (user units)
or /etc/containers/systemd/ (system units) alongside the generated <vmname>-compute.env file.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				vmFile = args[0]
			}

			convOpts := quadlet.DefaultOptions()
			convOpts.PasstWorkarounds = passtWorkarounds
			convOpts.Network = network

			opts := standalone.Options{
				LauncherImage:    launcherImage,
				AddVNCProxy:      vncProxy,
				VNCPort:          vncPort,
				VNCImage:         vncImage,
				AddSerialProxy:   serialProxy,
				SerialPort:       consolePort,
				SerialImage:      serialImage,
				PasstWorkarounds: passtWorkarounds,
				PasstDebug:       passtDebug,
			}

			return run(vmFile, opts, convOpts, outputDir)
		},
	}

	rootCmd.Flags().StringVar(&vmFile, "vm-file", "", "Path to VirtualMachine YAML file (reads stdin if omitted)")
	rootCmd.Flags().StringVar(&launcherImage, "launcher-image", "quay.io/kubevirt/virt-launcher:v1.9.0",
		"virt-launcher image reference")
	rootCmd.Flags().StringVar(&outputDir, "output-dir", "",
		"Directory to write Quadlet unit files into (prints to stdout when omitted)")
	rootCmd.Flags().BoolVar(&vncProxy, "vnc-proxy", false,
		"Inject a socat sidecar that forwards the VNC Unix socket to TCP --vnc-port")
	rootCmd.Flags().IntVar(&vncPort, "vnc-port", 5900,
		"TCP port for the VNC socat proxy (used when --vnc-proxy is set)")
	rootCmd.Flags().StringVar(&vncImage, "vnc-image", "docker.io/alpine/socat:latest",
		"Container image for the VNC socat proxy sidecar")
	rootCmd.Flags().BoolVar(&serialProxy, "console-proxy", false,
		"Inject a socat sidecar that forwards the serial console Unix socket to TCP --console-port")
	rootCmd.Flags().IntVar(&consolePort, "console-port", 2222,
		"TCP port for the serial console socat proxy (used when --console-proxy is set)")
	rootCmd.Flags().StringVar(&serialImage, "console-image", "docker.io/alpine/socat:latest",
		"Container image for the serial console socat proxy sidecar")
	rootCmd.Flags().BoolVar(&passtWorkarounds, "passt-workarounds", false,
		"Patch the passt.avx2 binary at pod startup to fix the mrg_rxbuf crash with 2+ vCPU guests (needed for virt-launcher images predating passt 0^20260611.ga9c61ff)")
	rootCmd.Flags().BoolVar(&passtDebug, "passt-debug", false,
		"Start passt with --debug --log-file /tmp/passt.log via a PATH wrapper (does not patch the binary; works with --passt-workarounds=false)")
	rootCmd.Flags().StringVar(&network, "network", "",
		"Quadlet Network= value for the VM pod (e.g. \"shared.network\" or \"host\"); omit to generate a dedicated per-VM .network unit for full isolation")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// run is the I/O wrapper: reads the VM YAML, runs the pure pipeline, writes output.
func run(vmFile string, opts standalone.Options, convOpts quadlet.Options, outputDir string) error {
	// Step 2: read and unmarshal VM YAML
	vm, err := readVM(vmFile)
	if err != nil {
		return err
	}

	files, err := convertVM(vm, opts, convOpts)
	if err != nil {
		return fmt.Errorf("failed to convert VM: %v", err)
	}

	return writeFiles(files, outputDir)
}

// convertVM is the pure pipeline: VirtualMachine struct in, Quadlet unit files out.
// Each line corresponds to one named pipeline stage.
func convertVM(vm *virtv1.VirtualMachine, opts standalone.Options, convOpts quadlet.Options) ([]quadlet.UnitFile, error) {
	// Step 3: OUR pre-render fixups (KubeVirt defaults + standalone tweaks)
	prepared, err := standalone.PrepareForRendering(vm, opts)
	if err != nil {
		return nil, fmt.Errorf("step 3 (pre-render): %w", err)
	}

	// Step 4: KubeVirt API — undecorated and crystal clear
	templateSvc := kubevirt.NewTemplateService(prepared.PVCCache, opts.LauncherImage)
	pod, err := templateSvc.RenderLaunchManifest(prepared.VMI)
	if err != nil {
		return nil, fmt.Errorf("step 4 (RenderLaunchManifest): %w", err)
	}

	// Step 5: OUR post-render fixups (Pod mutations for standalone/Podman)
	pod, err = standalone.AdaptForStandalone(pod, prepared, opts)
	if err != nil {
		return nil, fmt.Errorf("step 5 (post-render): %w", err)
	}

	// Step 6: vendored kube quadlet conversion
	files, err := quadlet.Convert(pod, convOpts)
	if err != nil {
		return nil, fmt.Errorf("step 6 (quadlet convert): %w", err)
	}

	// Step 7: OUR post-convert fixups (port publishing + passt workaround hook injection)
	files, err = standalone.ApplyPostConvertFixups(files, pod.Name, opts, convOpts)
	if err != nil {
		return nil, fmt.Errorf("step 7 (post-convert): %w", err)
	}

	return files, nil
}

// readVM reads and unmarshals a VirtualMachine YAML from a file path or stdin.
func readVM(vmFile string) (*virtv1.VirtualMachine, error) {
	var data []byte
	var err error

	if vmFile != "" && vmFile != "-" {
		data, err = os.ReadFile(vmFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read VM file: %v", err)
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read VM from stdin: %v", err)
		}
	}

	vm := &virtv1.VirtualMachine{}
	if err := yaml.Unmarshal(data, vm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal VM: %v%s", err, unmarshalErrorHint(err))
	}
	return vm, nil
}

// unmarshalErrorHint returns a short, actionable hint appended to a VM
// unmarshal error, or "" if err doesn't match a known cause. It checks each
// candidate cause via errors.Is/errors.As against the concrete error types
// k8s.io/apimachinery and encoding/json actually return, rather than matching
// substrings of err.Error() -- the sentinel/typed checks below survive being
// wrapped (with %w) by sigs.k8s.io/yaml's unmarshal chain, so they keep
// working even if the wrapping error text changes upstream.
func unmarshalErrorHint(err error) string {
	if hint := quantityErrorHint(err); hint != "" {
		return hint
	}
	return uint32OverflowHint(err)
}

// quantityErrorHint returns a hint for unmarshal errors caused by a malformed
// resource.Quantity value (e.g. a memory/CPU/storage value with an invalid
// unit suffix). resource.Quantity's UnmarshalJSON returns one of these
// sentinels directly, so errors.Is finds them however deep the error is
// wrapped. The JSON decoder does not preserve a field path for errors
// returned by a custom UnmarshalJSON, so the hint cannot point at the exact
// YAML field.
func quantityErrorHint(err error) string {
	switch {
	case errors.Is(err, resource.ErrSuffix),
		errors.Is(err, resource.ErrNumeric),
		errors.Is(err, resource.ErrFormatWrong):
		return ". Hint: check memory/CPU/storage values for a valid unit suffix " +
			"(e.g. domain.memory.guest, domain.resources.requests/limits) — valid examples: 512Mi, 2Gi, 1000m"
	default:
		return ""
	}
}

// uint32OverflowHint returns a hint for unmarshal errors caused by a negative
// number assigned to a uint32 field (e.g. domain.cpu.cores/sockets/threads,
// which must be a whole number >= 0 and can't represent -1). encoding/json
// reports this as a *json.UnmarshalTypeError, which -- unlike the Quantity
// case above -- does carry the exact field path, so the hint can name it.
func uint32OverflowHint(err error) string {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Type.Kind() != reflect.Uint32 {
		return ""
	}
	return fmt.Sprintf(". Hint: %s must be a whole number of 0 or more", typeErr.Field)
}

// writeFiles writes each UnitFile to outputDir, or streams them as a TAR
// archive to stdout when outputDir is empty (for machine consumption).
func writeFiles(files []quadlet.UnitFile, outputDir string) error {
	if outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return fmt.Errorf("failed to create output directory %q: %v", outputDir, err)
		}
		for _, f := range files {
			path := filepath.Join(outputDir, f.Name)
			perm := os.FileMode(0o644)
			if strings.HasSuffix(f.Name, ".sh") {
				perm = 0o755
			}
			if err := os.WriteFile(path, []byte(f.Content), perm); err != nil {
				return fmt.Errorf("failed to write %q: %v", path, err)
			}
			fmt.Fprintf(os.Stderr, "Written: %s\n", path)
		}
		return nil
	}

	return writeTAR(os.Stdout, files)
}

// writeTAR encodes the given unit files as a TAR archive written to w.
func writeTAR(w io.Writer, files []quadlet.UnitFile) error {
	tw := tar.NewWriter(w)
	for _, f := range files {
		data := []byte(f.Content)
		hdr := &tar.Header{
			Name: f.Name,
			Mode: 0o644,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("writing TAR header for %q: %w", f.Name, err)
		}
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("writing TAR content for %q: %w", f.Name, err)
		}
	}
	return tw.Close()
}
