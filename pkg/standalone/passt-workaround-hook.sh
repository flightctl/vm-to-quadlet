#!/bin/bash
# Libvirt QEMU hook — passt workarounds for pre-PR#18235 virt-launcher images.
# Enable via --passt-workarounds; not needed with passt >= 0^20260611.ga9c61ff.
#
# Called by libvirtd/virtqemud as:
#   /etc/libvirt/hooks/qemu <domain> <action> <state> [-]

ACTION="$2"
STATE="$3"

# Original KubeVirt migration handler.
if [ "$ACTION" = "release" ] && [ "$STATE" = "end" ] && [ "$4" = "migrated" ]; then
    touch /run/kubevirt-private/backend-storage-meta/migrated
fi

# Workarounds applied during domain prepare-begin.
# libvirt passes the domain XML on stdin; write modified XML to stdout.
# DO NOT call virsh here — it deadlocks virtqemud during prepare begin.
if [ "$ACTION" = "prepare" ] && [ "$STATE" = "begin" ]; then
    XML=$(cat)
    [ -z "$XML" ] && exit 0

    # Derive TCP port ranges from the VMI interface spec.
    TCP_PORTS=$(python3 -c "
import json, os
vmi = json.loads(os.environ.get('STANDALONE_VMI', '{}'))
out = []
for iface in vmi.get('spec',{}).get('domain',{}).get('devices',{}).get('interfaces',[]):
    for p in iface.get('ports', []):
        port = p.get('port')
        proto = (p.get('protocol') or 'TCP').upper()
        if port and proto == 'TCP':
            out.append(str(port))
print(' '.join(out))
" 2>/dev/null)

    if [ -n "$TCP_PORTS" ]; then
        RANGES=""
        for P in $TCP_PORTS; do
            RANGES="${RANGES}<range start=\"${P}\"/>"
        done
        TCP_FWD="<portForward proto=\"tcp\">${RANGES}</portForward>"
    else
        TCP_FWD=""
    fi

    # Patch 1: replace empty portForward elements and remove udp.
    PATCHED=$(printf '%s' "$XML" \
        | sed "s|<portForward proto='tcp'/>|${TCP_FWD}|g" \
        | sed "s|<portForward proto=\"tcp\"/>|${TCP_FWD}|g" \
        | grep -v "portForward proto=.udp.")

    # Patch 2: disable mrg_rxbuf via qemu:commandline to prevent passt crash
    # with 2+ vCPU guests (libvirt silently strips <driver mrg_rxbuf='off'/>
    # for passt-backend interfaces).
    if ! printf '%s' "$PATCHED" | grep -q "mrg_rxbuf"; then
        PATCHED=$(printf '%s' "$PATCHED" \
            | sed "s|<domain type='kvm' id='[0-9]*'|<domain type='kvm'|")
        if ! printf '%s' "$PATCHED" | grep -q "xmlns:qemu"; then
            PATCHED=$(printf '%s' "$PATCHED" \
                | sed "s|<domain type='kvm'|<domain type='kvm' xmlns:qemu='http://libvirt.org/schemas/domain/qemu/1.0'|")
        fi
        PATCHED=$(printf '%s' "$PATCHED" \
            | sed "s|</domain>|<qemu:commandline><qemu:arg value='-set'/><qemu:arg value='device.ua-default.mrg_rxbuf=off'/></qemu:commandline></domain>|")
    fi

    printf '%s\n' "$PATCHED"
fi

exit 0
