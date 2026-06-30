package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
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
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				vmFile = args[0]
			}

			convOpts := quadlet.DefaultOptions()
			convOpts.PasstWorkarounds = passtWorkarounds

			opts := standalone.Options{
				LauncherImage:    launcherImage,
				AddVNCProxy:      vncProxy,
				VNCPort:          vncPort,
				VNCImage:         vncImage,
				AddSerialProxy:   serialProxy,
				SerialPort:       consolePort,
				SerialImage:      serialImage,
				PasstWorkarounds: passtWorkarounds,
			}


			return run(vmFile, opts, convOpts, outputDir)
		},
	}

	rootCmd.Flags().StringVar(&vmFile, "vm-file", "", "Path to VirtualMachine YAML file (reads stdin if omitted)")
	rootCmd.Flags().StringVar(&launcherImage, "launcher-image", "quay.io/kubevirt/virt-launcher:v1.8.4",
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
		return nil, fmt.Errorf("failed to unmarshal VM: %v", err)
	}
	return vm, nil
}

// writeFiles writes each UnitFile to outputDir, or prints them to stdout
// separated by "### <filename>" markers when outputDir is empty.
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

	for i, f := range files {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("### %s\n", f.Name)
		fmt.Print(f.Content)
	}
	return nil
}
