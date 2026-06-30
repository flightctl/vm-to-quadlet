package quadlet

// UnitFile is a single generated Quadlet file with its intended filename and rendered content.
type UnitFile struct {
	Name    string // e.g. "fedora-vm-compute.container"
	Content string // full INI text
}

// UnitSection represents the [Unit] INI section shared by all unit types.
type UnitSection struct {
	Description string
	After       []string
	Requires    []string
}

// ContainerSection represents the [Container] INI section of a .container file.
// Fields map 1:1 to Quadlet directives as documented in podman-systemd.unit(5).
type ContainerSection struct {
	Image            string
	Entrypoint       string // overrides the image ENTRYPOINT; use when the image has a default entrypoint that must not be used
	Exec             string
	Environments     []string // KEY=VALUE pairs, emitted as separate Environment= lines
	EnvironmentFiles []string // paths to env files, emitted as EnvironmentFile= lines
	Volumes          []string // src:dst[:opts], emitted as separate Volume= lines
	Mounts           []string // type=...,src=...,dst=..., emitted as separate Mount= lines
	AddDevices       []string // host device paths, emitted as AddDevice= lines
	AddCapabilities  []string
	DropCapabilities []string
	NoNewPrivileges  bool
	User             string
	PublishPorts     []string // hostPort:containerPort or just containerPort
	MemoryLimit      string   // bytes as string
	Network          string   // e.g. "podman" or "container:<name>" for netns sharing
	VolumesFrom      []string // container names to inherit volumes from (e.g. "systemd-<compute>")
	Sysctls          []string // name=value pairs
	StopTimeout      int      // seconds to wait for graceful shutdown before SIGKILL (0 = use Podman default)
}

// ServiceSection represents the [Service] INI section.
type ServiceSection struct {
	Type            string // "oneshot" for init containers
	RemainAfterExit bool
	Restart         string // "on-failure", "always", "no"
}

// InstallSection represents the [Install] INI section.
type InstallSection struct {
	WantedBy []string
}

// ContainerUnit is a fully-defined .container Quadlet unit.
type ContainerUnit struct {
	Unit      UnitSection
	Container ContainerSection
	Service   ServiceSection
	Install   InstallSection
}

// VolumeSection represents the [Volume] INI section of a .volume file.
type VolumeSection struct {
	// Driver sets the volume driver (e.g. "image" for OCI image-backed volumes).
	Driver string
	// Image is the image reference or a .image unit filename for Driver=image volumes.
	// When it ends with ".image" Quadlet auto-wires a dependency on the image service.
	Image string
	// VolumeName overrides the default "systemd-<unitname>" Podman volume name.
	VolumeName string
}

// VolumeUnit is a fully-defined .volume Quadlet unit.
type VolumeUnit struct {
	Unit   UnitSection
	Volume VolumeSection
}

// Options controls the Pod→Quadlet conversion behaviour.
type Options struct {
	// Network is the Quadlet Network= value for the compute container.
	// Empty string (the default) omits the directive and lets Podman use its default
	// networking (bridge for root, slirp4netns/pasta for rootless).
	// KubeVirt's passt network binding runs inside the VM networking stack and is
	// unrelated to the Podman container network mode — do not set this to "pasta".
	Network string

	// PasstWorkarounds enables the passt.avx2 binary patch init container that
	// prevents a passt crash with 2+ vCPU guests due to the mrg_rxbuf scattergather
	// overflow bug (fixed upstream in passt 0^20260611.ga9c61ff / PR #18235).
	// The init container patches the binary from the virt-launcher image at pod
	// startup and exposes it via /passt-bin in the compute container's PATH.
	// Enable this only when running against a virt-launcher image that predates
	// passt 0^20260611.ga9c61ff.
	PasstWorkarounds bool
}

// DefaultOptions returns Options with sensible defaults for standalone VM execution.
//
// Network is left empty so Convert() generates a per-VM <vmname>.network Quadlet unit,
// giving each VM its own isolated Podman bridge network. Set Network explicitly to share
// a network across VMs or to use a pre-existing named network.
func DefaultOptions() Options {
	return Options{}
}
