package quadlet

import (
	"bytes"
	"fmt"
)

// RenderContainerUnit serializes a ContainerUnit to its INI text representation.
// Sections with no content are omitted. Multi-value directives (Environment=,
// Volume=, etc.) each emit a separate line as required by Quadlet.
func RenderContainerUnit(u ContainerUnit) string {
	var b bytes.Buffer

	writeUnitSection(&b, u.Unit)
	writeContainerSection(&b, u.Container)
	writeServiceSection(&b, u.Service)
	writeInstallSection(&b, u.Install)

	return b.String()
}

// RenderVolumeUnit serializes a VolumeUnit to its INI text representation.
func RenderVolumeUnit(u VolumeUnit) string {
	var b bytes.Buffer

	writeUnitSection(&b, u.Unit)

	fmt.Fprintln(&b, "[Volume]")
	if u.Volume.Driver != "" {
		fmt.Fprintf(&b, "Driver=%s\n", u.Volume.Driver)
	}
	if u.Volume.Image != "" {
		fmt.Fprintf(&b, "Image=%s\n", u.Volume.Image)
	}
	if u.Volume.VolumeName != "" {
		fmt.Fprintf(&b, "VolumeName=%s\n", u.Volume.VolumeName)
	}
	fmt.Fprintln(&b)

	return b.String()
}

func writeUnitSection(b *bytes.Buffer, u UnitSection) {
	if u.Description == "" && len(u.After) == 0 && len(u.Requires) == 0 {
		return
	}
	fmt.Fprintln(b, "[Unit]")
	if u.Description != "" {
		fmt.Fprintf(b, "Description=%s\n", u.Description)
	}
	for _, a := range u.After {
		fmt.Fprintf(b, "After=%s\n", a)
	}
	for _, r := range u.Requires {
		fmt.Fprintf(b, "Requires=%s\n", r)
	}
	fmt.Fprintln(b)
}

func writeContainerSection(b *bytes.Buffer, c ContainerSection) {
	fmt.Fprintln(b, "[Container]")
	if c.Image != "" {
		fmt.Fprintf(b, "Image=%s\n", c.Image)
	}
	if c.Entrypoint != "" {
		fmt.Fprintf(b, "Entrypoint=%s\n", c.Entrypoint)
	}
	if c.Exec != "" {
		fmt.Fprintf(b, "Exec=%s\n", c.Exec)
	}
	for _, ef := range c.EnvironmentFiles {
		fmt.Fprintf(b, "EnvironmentFile=%s\n", ef)
	}
	for _, env := range c.Environments {
		fmt.Fprintf(b, "Environment=%s\n", env)
	}
	for _, vol := range c.Volumes {
		fmt.Fprintf(b, "Volume=%s\n", vol)
	}
	for _, mnt := range c.Mounts {
		fmt.Fprintf(b, "Mount=%s\n", mnt)
	}
	for _, dev := range c.AddDevices {
		fmt.Fprintf(b, "AddDevice=%s\n", dev)
	}
	for _, cap := range c.AddCapabilities {
		fmt.Fprintf(b, "AddCapability=%s\n", cap)
	}
	for _, cap := range c.DropCapabilities {
		fmt.Fprintf(b, "DropCapability=%s\n", cap)
	}
	if c.NoNewPrivileges {
		fmt.Fprintln(b, "NoNewPrivileges=true")
	}
	if c.User != "" {
		fmt.Fprintf(b, "User=%s\n", c.User)
	}
	for _, port := range c.PublishPorts {
		fmt.Fprintf(b, "PublishPort=%s\n", port)
	}
	if c.MemoryLimit != "" {
		fmt.Fprintf(b, "MemoryLimit=%s\n", c.MemoryLimit)
	}
	if c.Network != "" {
		fmt.Fprintf(b, "Network=%s\n", c.Network)
	}
	for _, vf := range c.VolumesFrom {
		fmt.Fprintf(b, "PodmanArgs=--volumes-from %s\n", vf)
	}
	if c.StopTimeout > 0 {
		fmt.Fprintf(b, "StopTimeout=%d\n", c.StopTimeout)
	}
	for _, sysctl := range c.Sysctls {
		fmt.Fprintf(b, "Sysctl=%s\n", sysctl)
	}
	fmt.Fprintln(b)
}

func writeServiceSection(b *bytes.Buffer, s ServiceSection) {
	if s.Type == "" && !s.RemainAfterExit && s.Restart == "" {
		return
	}
	fmt.Fprintln(b, "[Service]")
	if s.Type != "" {
		fmt.Fprintf(b, "Type=%s\n", s.Type)
	}
	if s.RemainAfterExit {
		fmt.Fprintln(b, "RemainAfterExit=yes")
	}
	if s.Restart != "" {
		fmt.Fprintf(b, "Restart=%s\n", s.Restart)
	}
	fmt.Fprintln(b)
}

func writeInstallSection(b *bytes.Buffer, i InstallSection) {
	if len(i.WantedBy) == 0 {
		return
	}
	fmt.Fprintln(b, "[Install]")
	for _, w := range i.WantedBy {
		fmt.Fprintf(b, "WantedBy=%s\n", w)
	}
}
