package metrics

// Snapshot is a single point-in-time reading of the whole system. It is the
// unit that gets serialized to JSON and pushed to the browser over SSE.
type Snapshot struct {
	TS      int64        `json:"ts"` // unix millis
	Host    HostInfo     `json:"host"`
	CPU     CPUStats     `json:"cpu"`
	Memory  MemoryStats  `json:"memory"`
	Swap    SwapStats    `json:"swap"`
	Load    LoadStats    `json:"load"`
	Disks   []DiskStats  `json:"disks"`
	Network []NetStats   `json:"network"`
	Procs   []ProcStats  `json:"procs"`
	Sensors []Sensor     `json:"sensors"`
	Docker  []Container  `json:"docker"`
}

type HostInfo struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Platform     string `json:"platform"`
	PlatformVer  string `json:"platformVersion"`
	Kernel       string `json:"kernel"`
	Arch         string `json:"arch"`
	Virt         string `json:"virtualization"`
	Uptime       uint64 `json:"uptime"`    // seconds
	BootTime     uint64 `json:"bootTime"`  // unix seconds
	ProcCount    uint64 `json:"procCount"`
	CPUModel     string `json:"cpuModel"`
	CPUPhysical  int    `json:"cpuPhysical"`
	CPULogical   int    `json:"cpuLogical"`
	CPUMHz       float64 `json:"cpuMHz"`
}

type CPUStats struct {
	Total   float64   `json:"total"`   // aggregate % 0..100
	PerCore []float64 `json:"perCore"` // per logical core %
}

type MemoryStats struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	Available   uint64  `json:"available"`
	Cached      uint64  `json:"cached"`
	Buffers     uint64  `json:"buffers"`
	UsedPercent float64 `json:"usedPercent"`
}

type SwapStats struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	UsedPercent float64 `json:"usedPercent"`
}

type LoadStats struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

type DiskStats struct {
	Device      string  `json:"device"`
	Mount       string  `json:"mount"`
	Fstype      string  `json:"fstype"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	UsedPercent float64 `json:"usedPercent"`
	ReadRate    float64 `json:"readRate"`  // bytes/sec
	WriteRate   float64 `json:"writeRate"` // bytes/sec
}

type NetStats struct {
	Name     string  `json:"name"`
	RxBytes  uint64  `json:"rxBytes"`
	TxBytes  uint64  `json:"txBytes"`
	RxRate   float64 `json:"rxRate"` // bytes/sec
	TxRate   float64 `json:"txRate"` // bytes/sec
}

type ProcStats struct {
	PID     int32   `json:"pid"`
	Name    string  `json:"name"`
	User    string  `json:"user"`
	Status  string  `json:"status"`
	CPU     float64 `json:"cpu"` // % of one core normalized to all cores
	MemPct  float64 `json:"memPct"`
	RSS     uint64  `json:"rss"`
}

type Sensor struct {
	Key      string  `json:"key"`
	Temp     float64 `json:"temp"`
	High     float64 `json:"high"`
	Critical float64 `json:"critical"`
}

type Container struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Image    string  `json:"image"`
	State    string  `json:"state"`
	Status   string  `json:"status"`
	CPU      float64 `json:"cpu"`
	MemUsage uint64  `json:"memUsage"`
	MemLimit uint64  `json:"memLimit"`
	MemPct   float64 `json:"memPct"`
}
