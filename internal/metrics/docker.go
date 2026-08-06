package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// dockerClient is a tiny Docker Engine API client that speaks HTTP over the
// unix socket. It intentionally avoids the official SDK to keep the binary
// small and dependency-light — we only need two endpoints.
type dockerClient struct {
	http *http.Client
}

func newDockerClient(socket string) *dockerClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &dockerClient{http: &http.Client{Transport: tr, Timeout: 4 * time.Second}}
}

func (d *dockerClient) get(path string, out any) error {
	// Host is ignored for unix sockets but required to form a valid URL.
	req, err := http.NewRequest(http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("docker: %s -> %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d *dockerClient) ping() bool {
	var v map[string]any
	return d.get("/v1.41/version", &v) == nil
}

// dockerContainer mirrors the subset of GET /containers/json we care about.
type dockerContainer struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

// dockerStats mirrors the subset of GET /containers/{id}/stats we care about.
type dockerStats struct {
	CPUStats    cpuStats `json:"cpu_stats"`
	PreCPUStats cpuStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

type cpuStats struct {
	CPUUsage struct {
		Total  uint64 `json:"total_usage"`
		PerCPU []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint64 `json:"online_cpus"`
}

func (d *dockerClient) containers() ([]Container, error) {
	var raw []dockerContainer
	if err := d.get("/v1.41/containers/json?all=1", &raw); err != nil {
		return nil, err
	}

	out := make([]Container, len(raw))
	var wg sync.WaitGroup
	for i, rc := range raw {
		name := strings.TrimPrefix(firstName(rc.Names), "/")
		c := Container{
			ID:     shortID(rc.ID),
			Name:   name,
			Image:  rc.Image,
			State:  rc.State,
			Status: rc.Status,
		}
		out[i] = c
		if rc.State != "running" {
			continue
		}
		// Fetch per-container stats concurrently; each is a one-shot read.
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			var st dockerStats
			if err := d.get("/v1.41/containers/"+id+"/stats?stream=false", &st); err != nil {
				return
			}
			out[idx].CPU = round2(dockerCPUPercent(st))
			usage := st.MemoryStats.Usage
			if cache, ok := st.MemoryStats.Stats["inactive_file"]; ok && usage >= cache {
				usage -= cache // match `docker stats` accounting
			}
			out[idx].MemUsage = usage
			out[idx].MemLimit = st.MemoryStats.Limit
			if st.MemoryStats.Limit > 0 {
				out[idx].MemPct = round2(float64(usage) / float64(st.MemoryStats.Limit) * 100)
			}
		}(i, rc.ID)
	}
	wg.Wait()
	return out, nil
}

func dockerCPUPercent(st dockerStats) float64 {
	cpuDelta := float64(st.CPUStats.CPUUsage.Total) - float64(st.PreCPUStats.CPUUsage.Total)
	sysDelta := float64(st.CPUStats.SystemUsage) - float64(st.PreCPUStats.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		return 0
	}
	cpus := float64(st.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(st.CPUStats.CPUUsage.PerCPU))
	}
	if cpus == 0 {
		cpus = 1
	}
	return (cpuDelta / sysDelta) * cpus * 100
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
