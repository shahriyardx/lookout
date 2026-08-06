package metrics

import (
	"sort"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/shirou/gopsutil/v4/sensors"
)

// Collector gathers system metrics. It is stateful: rate-based metrics
// (network throughput, disk I/O, per-process CPU) are derived from the delta
// between the current reading and the previous one, so a single Collector
// instance must be reused across polls.
type Collector struct {
	mu sync.Mutex

	topN         int
	dockerClient *dockerClient

	// static host facts, resolved once
	staticHost HostInfo
	hostOnce   sync.Once

	// previous counters for rate computation
	lastTime  time.Time
	lastNet   map[string]net.IOCountersStat
	lastDisk  map[string]disk.IOCountersStat
	lastProc  map[int32]procSample
	logical   int
}

type procSample struct {
	cpuSeconds float64
	seen       bool
}

// New builds a Collector. topN caps how many processes are returned (ranked by
// CPU). If dockerSock is non-empty and reachable, a Docker tab is populated.
func New(topN int, dockerSock string) *Collector {
	c := &Collector{
		topN:     topN,
		lastNet:  map[string]net.IOCountersStat{},
		lastDisk: map[string]disk.IOCountersStat{},
		lastProc: map[int32]procSample{},
	}
	c.logical, _ = cpu.Counts(true)
	if c.logical <= 0 {
		c.logical = 1
	}
	if dockerSock != "" {
		if dc := newDockerClient(dockerSock); dc != nil && dc.ping() {
			c.dockerClient = dc
		}
	}
	// Prime cpu.Percent so the first real Collect returns a meaningful value.
	_, _ = cpu.Percent(0, false)
	_, _ = cpu.Percent(0, true)
	return c
}

// DockerEnabled reports whether a reachable Docker socket was found.
func (c *Collector) DockerEnabled() bool { return c.dockerClient != nil }

func (c *Collector) resolveStaticHost() {
	c.hostOnce.Do(func() {
		h := HostInfo{Arch: "unknown"}
		if info, err := host.Info(); err == nil {
			h.Hostname = info.Hostname
			h.OS = info.OS
			h.Platform = info.Platform
			h.PlatformVer = info.PlatformVersion
			h.Kernel = info.KernelVersion
			h.Arch = info.KernelArch
			h.Virt = info.VirtualizationSystem
			h.BootTime = info.BootTime
		}
		if ci, err := cpu.Info(); err == nil && len(ci) > 0 {
			h.CPUModel = ci[0].ModelName
			h.CPUMHz = ci[0].Mhz
		}
		h.CPUPhysical, _ = cpu.Counts(false)
		h.CPULogical, _ = cpu.Counts(true)
		c.staticHost = h
	})
}

// Collect produces one Snapshot. Safe for concurrent use.
func (c *Collector) Collect() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.resolveStaticHost()
	now := time.Now()
	elapsed := now.Sub(c.lastTime).Seconds()
	if c.lastTime.IsZero() || elapsed <= 0 {
		elapsed = 0 // first run: no rates yet
	}

	snap := Snapshot{
		TS:     now.UnixMilli(),
		Host:   c.staticHost,
		Docker: []Container{},
	}

	// Live host facts (uptime, proc count) refreshed each poll.
	if info, err := host.Info(); err == nil {
		snap.Host.Uptime = info.Uptime
		snap.Host.ProcCount = info.Procs
	}

	c.collectCPU(&snap)
	c.collectMem(&snap)
	c.collectLoad(&snap)
	c.collectDisk(&snap, elapsed)
	c.collectNet(&snap, elapsed)
	c.collectProcs(&snap, elapsed)
	c.collectSensors(&snap)

	if c.dockerClient != nil {
		if cs, err := c.dockerClient.containers(); err == nil {
			snap.Docker = cs
		}
	}

	c.lastTime = now
	return snap
}

func (c *Collector) collectCPU(snap *Snapshot) {
	if agg, err := cpu.Percent(0, false); err == nil && len(agg) > 0 {
		snap.CPU.Total = round2(agg[0])
	}
	if per, err := cpu.Percent(0, true); err == nil {
		snap.CPU.PerCore = make([]float64, len(per))
		for i, v := range per {
			snap.CPU.PerCore[i] = round2(v)
		}
	}
}

func (c *Collector) collectMem(snap *Snapshot) {
	if v, err := mem.VirtualMemory(); err == nil {
		snap.Memory = MemoryStats{
			Total:       v.Total,
			Used:        v.Used,
			Free:        v.Free,
			Available:   v.Available,
			Cached:      v.Cached,
			Buffers:     v.Buffers,
			UsedPercent: round2(v.UsedPercent),
		}
	}
	if s, err := mem.SwapMemory(); err == nil {
		snap.Swap = SwapStats{
			Total:       s.Total,
			Used:        s.Used,
			Free:        s.Free,
			UsedPercent: round2(s.UsedPercent),
		}
	}
}

func (c *Collector) collectLoad(snap *Snapshot) {
	if l, err := load.Avg(); err == nil {
		snap.Load = LoadStats{
			Load1:  round2(l.Load1),
			Load5:  round2(l.Load5),
			Load15: round2(l.Load15),
		}
	}
}

func (c *Collector) collectDisk(snap *Snapshot, elapsed float64) {
	parts, err := disk.Partitions(false)
	if err != nil {
		return
	}
	// Aggregate I/O rates across all physical devices onto their mounts is
	// non-trivial; instead we expose per-mount usage and per-device I/O rate
	// keyed by the partition's underlying device name.
	io, _ := disk.IOCounters()
	seenMount := map[string]bool{}
	for _, p := range parts {
		if isSkippableFs(p.Fstype) || seenMount[p.Mountpoint] {
			continue
		}
		seenMount[p.Mountpoint] = true
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		d := DiskStats{
			Device:      p.Device,
			Mount:       p.Mountpoint,
			Fstype:      p.Fstype,
			Total:       u.Total,
			Used:        u.Used,
			Free:        u.Free,
			UsedPercent: round2(u.UsedPercent),
		}
		if dev := baseDev(p.Device); dev != "" {
			if cur, ok := io[dev]; ok {
				if elapsed > 0 {
					if prev, ok := c.lastDisk[dev]; ok {
						d.ReadRate = rate(cur.ReadBytes, prev.ReadBytes, elapsed)
						d.WriteRate = rate(cur.WriteBytes, prev.WriteBytes, elapsed)
					}
				}
			}
		}
		snap.Disks = append(snap.Disks, d)
	}
	// stash current io counters for next delta
	for k, v := range io {
		c.lastDisk[k] = v
	}
}

func (c *Collector) collectNet(snap *Snapshot, elapsed float64) {
	counters, err := net.IOCounters(true)
	if err != nil {
		return
	}
	for _, n := range counters {
		if isSkippableIface(n.Name) {
			continue
		}
		ns := NetStats{
			Name:    n.Name,
			RxBytes: n.BytesRecv,
			TxBytes: n.BytesSent,
		}
		if elapsed > 0 {
			if prev, ok := c.lastNet[n.Name]; ok {
				ns.RxRate = rate(n.BytesRecv, prev.BytesRecv, elapsed)
				ns.TxRate = rate(n.BytesSent, prev.BytesSent, elapsed)
			}
		}
		snap.Network = append(snap.Network, ns)
		c.lastNet[n.Name] = n
	}
	sort.Slice(snap.Network, func(i, j int) bool {
		return snap.Network[i].Name < snap.Network[j].Name
	})
}

func (c *Collector) collectProcs(snap *Snapshot, elapsed float64) {
	procs, err := process.Processes()
	if err != nil {
		return
	}
	current := make(map[int32]procSample, len(procs))
	rows := make([]ProcStats, 0, len(procs))

	for _, p := range procs {
		times, err := p.Times()
		if err != nil {
			continue
		}
		cpuSeconds := times.User + times.System
		current[p.Pid] = procSample{cpuSeconds: cpuSeconds, seen: true}

		var cpuPct float64
		if elapsed > 0 {
			if prev, ok := c.lastProc[p.Pid]; ok {
				delta := cpuSeconds - prev.cpuSeconds
				if delta > 0 {
					// normalize across all logical cores => 0..100 of the box
					cpuPct = (delta / elapsed) / float64(c.logical) * 100
				}
			}
		}

		name, _ := p.Name()
		memPct, _ := p.MemoryPercent()
		var rss uint64
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = mi.RSS
		}
		user, _ := p.Username()
		status := ""
		if st, err := p.Status(); err == nil && len(st) > 0 {
			status = st[0]
		}

		rows = append(rows, ProcStats{
			PID:    p.Pid,
			Name:   name,
			User:   user,
			Status: status,
			CPU:    round2(cpuPct),
			MemPct: round2(float64(memPct)),
			RSS:    rss,
		})
	}
	c.lastProc = current

	// Candidate pool = union of the top processes by CPU and by RSS. Sending
	// both dimensions means the client can sort by either and still show the
	// true leaders — a memory hog that's idle on CPU still appears, and a CPU
	// spinner with a tiny footprint isn't dropped when sorting by memory.
	pool := c.topN
	if pool < 30 {
		pool = 30
	}
	picked := make(map[int32]bool, pool*2)
	snap.Procs = make([]ProcStats, 0, pool*2)

	sort.Slice(rows, func(i, j int) bool { return rows[i].CPU > rows[j].CPU })
	for i := 0; i < len(rows) && i < pool; i++ {
		if !picked[rows[i].PID] {
			picked[rows[i].PID] = true
			snap.Procs = append(snap.Procs, rows[i])
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RSS > rows[j].RSS })
	for i := 0; i < len(rows) && i < pool; i++ {
		if !picked[rows[i].PID] {
			picked[rows[i].PID] = true
			snap.Procs = append(snap.Procs, rows[i])
		}
	}
	// Default order is CPU-descending; the client re-sorts on demand.
	sort.Slice(snap.Procs, func(i, j int) bool { return snap.Procs[i].CPU > snap.Procs[j].CPU })
}

func (c *Collector) collectSensors(snap *Snapshot) {
	temps, err := sensors.SensorsTemperatures()
	if err != nil {
		return
	}
	for _, t := range temps {
		if t.Temperature <= 0 {
			continue
		}
		snap.Sensors = append(snap.Sensors, Sensor{
			Key:      t.SensorKey,
			Temp:     round2(t.Temperature),
			High:     t.High,
			Critical: t.Critical,
		})
	}
}
