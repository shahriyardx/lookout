package metrics

import (
	"math"
	"strings"
)

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

// rate returns a non-negative per-second rate from two monotonic counters.
// Counter resets (cur < prev) yield 0 rather than a negative spike.
func rate(cur, prev uint64, elapsed float64) float64 {
	if elapsed <= 0 || cur < prev {
		return 0
	}
	return round2(float64(cur-prev) / elapsed)
}

// baseDev strips a partition path down to the disk I/O counter key.
// e.g. /dev/sda2 -> sda2 ; /dev/mapper/vg-root -> vg-root ; C: -> C:
func baseDev(device string) string {
	if device == "" {
		return ""
	}
	device = strings.TrimPrefix(device, "/dev/")
	if i := strings.LastIndex(device, "/"); i >= 0 {
		device = device[i+1:]
	}
	return device
}

// isSkippableFs drops pseudo/virtual filesystems that would otherwise clutter
// the disk view (e.g. macOS devfs at a bogus 100%, or Linux overlay/tmpfs).
func isSkippableFs(fstype string) bool {
	switch strings.ToLower(fstype) {
	case "devfs", "autofs", "overlay", "squashfs", "tracefs", "debugfs",
		"proc", "sysfs", "cgroup", "cgroup2", "mqueue", "fusectl",
		"binfmt_misc", "devpts", "none":
		return true
	}
	return false
}

// isSkippableIface filters out loopback and virtual bridge/veth noise so the
// network view stays focused on real interfaces.
func isSkippableIface(name string) bool {
	if name == "lo" || name == "lo0" {
		return true
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr", "utun", "llw", "awdl", "gif", "stf", "anpi", "ap"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
