// Package sysinfo reports the state of the server: CPU, memory, disks, network, GPUs and
// containers. Host metrics are read from /proc (Linux), GPUs from nvidia-smi and containers
// from the docker CLI.
package sysinfo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Report is a snapshot of the server.
type Report struct {
	Host       Host        `json:"host"`
	CPU        CPU         `json:"cpu"`
	Memory     Memory      `json:"memory"`
	Disks      []Disk      `json:"disks"`
	Network    []Interface `json:"network"`
	GPUs       []GPU       `json:"gpus"`
	Containers Containers  `json:"containers"`
	// Errors lists what could not be read, the rest of the report stays usable.
	Errors []string `json:"errors,omitempty"`
}

type Host struct {
	Name   string     `json:"name"`
	OS     string     `json:"os"`
	Kernel string     `json:"kernel"`
	Uptime int64      `json:"uptime_seconds"`
	Load   [3]float64 `json:"load"`
}

type CPU struct {
	Cores int `json:"cores"`
	// Usage is the busy share of all cores over the sampling interval, 0 to 100.
	Usage float64 `json:"usage"`
}

type Memory struct {
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
	SwapTotal uint64 `json:"swap_total"`
	SwapFree  uint64 `json:"swap_free"`
}

type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// Interface is a network interface: rates over the sampling interval, totals since boot.
type Interface struct {
	Name    string  `json:"name"`
	RxRate  float64 `json:"rx_rate"`
	TxRate  float64 `json:"tx_rate"`
	RxTotal uint64  `json:"rx_total"`
	TxTotal uint64  `json:"tx_total"`
}

type GPU struct {
	Name        string  `json:"name"`
	Usage       float64 `json:"usage"`
	MemoryUsed  uint64  `json:"memory_used"`
	MemoryTotal uint64  `json:"memory_total"`
	Temperature float64 `json:"temperature"`
}

type Containers struct {
	Running   int         `json:"running"`
	Stopped   int         `json:"stopped"`
	Unhealthy int         `json:"unhealthy"`
	List      []Container `json:"list"`
}

type Container struct {
	Name   string `json:"name"`
	Stack  string `json:"stack"`
	State  string `json:"state"`
	Status string `json:"status"`
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	NetIO  string `json:"net_io,omitempty"`
}

// Collect samples the server over interval (CPU and network rates).
func Collect(ctx context.Context, dockerBin string, interval time.Duration) *Report {
	r := &Report{}
	var mu sync.Mutex
	fail := func(what string, err error) {
		mu.Lock()
		defer mu.Unlock()
		r.Errors = append(r.Errors, what+": "+err.Error())
	}
	r.Host.Name, _ = os.Hostname()
	r.CPU.Cores = runtime.NumCPU()
	if runtime.GOOS != "linux" {
		fail("host", errors.New("host metrics are read from /proc, on Linux only"))
	} else {
		r.Host.OS = osName()
		if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			r.Host.Kernel = strings.TrimSpace(string(b))
		}
		if b, err := os.ReadFile("/proc/uptime"); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 {
				s, _ := strconv.ParseFloat(f[0], 64)
				r.Host.Uptime = int64(s)
			}
		}
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			r.Host.Load = parseLoad(string(b))
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			r.Memory = parseMeminfo(string(b))
		} else {
			fail("memory", err)
		}
	}

	var wg sync.WaitGroup
	if runtime.GOOS == "linux" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cpu0, err1 := os.ReadFile("/proc/stat")
			net0, err2 := os.ReadFile("/proc/net/dev")
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			cpu1, err3 := os.ReadFile("/proc/stat")
			net1, err4 := os.ReadFile("/proc/net/dev")
			if err := errors.Join(err1, err3); err != nil {
				fail("cpu", err)
			} else {
				r.CPU.Usage = cpuUsage(parseCPUStat(string(cpu0)), parseCPUStat(string(cpu1)))
			}
			if err := errors.Join(err2, err4); err != nil {
				fail("network", err)
			} else {
				r.Network = netRates(parseNetDev(string(net0)), parseNetDev(string(net1)), interval)
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		gpus, err := nvidiaGPUs(ctx)
		if err != nil {
			fail("gpu", err)
		}
		r.GPUs = gpus
	}()
	go func() {
		defer wg.Done()
		cs, err := containers(ctx, dockerBin)
		if err != nil {
			fail("containers", err)
		}
		r.Containers = cs
	}()
	r.Disks = disks(ctx, dockerBin)
	wg.Wait()
	sort.Strings(r.Errors)
	return r
}

func osName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func parseLoad(s string) [3]float64 {
	var out [3]float64
	f := strings.Fields(s)
	for i := 0; i < 3 && i < len(f); i++ {
		out[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return out
}

func parseMeminfo(s string) Memory {
	kb := map[string]uint64{}
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) > 0 {
			n, _ := strconv.ParseUint(f[0], 10, 64)
			kb[name] = n * 1024
		}
	}
	return Memory{Total: kb["MemTotal"], Available: kb["MemAvailable"], SwapTotal: kb["SwapTotal"], SwapFree: kb["SwapFree"]}
}

// cpuTimes is the aggregated line of /proc/stat: busy and total jiffies.
type cpuTimes struct{ busy, total uint64 }

func parseCPUStat(s string) cpuTimes {
	line, _, _ := strings.Cut(s, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}
	}
	var t cpuTimes
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		t.total += n
		if i != 3 && i != 4 { // idle and iowait
			t.busy += n
		}
	}
	return t
}

func cpuUsage(a, b cpuTimes) float64 {
	if b.total <= a.total {
		return 0
	}
	return 100 * float64(b.busy-a.busy) / float64(b.total-a.total)
}

// virtualIface matches the interfaces docker creates, left out of the report.
func virtualIface(name string) bool {
	for _, p := range []string{"lo", "veth", "docker", "br-", "vxlan", "cali", "flannel", "tun", "virbr"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return name == "docker_gwbridge"
}

func parseNetDev(s string) map[string][2]uint64 {
	out := map[string][2]uint64{}
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if len(f) < 9 || virtualIface(name) {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		if rx == 0 && tx == 0 { // unused tunnel devices of the kernel (gre0, sit0...)
			continue
		}
		out[name] = [2]uint64{rx, tx}
	}
	return out
}

func netRates(a, b map[string][2]uint64, interval time.Duration) []Interface {
	var out []Interface
	for name, now := range b {
		before, ok := a[name]
		it := Interface{Name: name, RxTotal: now[0], TxTotal: now[1]}
		if ok && interval > 0 && now[0] >= before[0] && now[1] >= before[1] {
			it.RxRate = float64(now[0]-before[0]) / interval.Seconds()
			it.TxRate = float64(now[1]-before[1]) / interval.Seconds()
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RxTotal+out[i].TxTotal > out[j].RxTotal+out[j].TxTotal })
	return out
}

func disks(ctx context.Context, dockerBin string) []Disk {
	paths := []string{"/"}
	if out, err := exec.CommandContext(ctx, dockerBin, "info", "--format", "{{.DockerRootDir}}").Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			paths = append(paths, p)
		}
	}
	var out []Disk
	seen := map[uint64]bool{}
	for _, p := range paths {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err != nil {
			continue
		}
		id := uint64(st.Blocks)<<20 ^ uint64(st.Bfree)
		if seen[id] { // same filesystem
			continue
		}
		seen[id] = true
		bs := uint64(st.Bsize)
		out = append(out, Disk{Path: p, Total: uint64(st.Blocks) * bs, Free: uint64(st.Bavail) * bs})
	}
	return out
}

func nvidiaGPUs(ctx context.Context) ([]GPU, error) {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, nil // no NVIDIA GPU
	}
	out, err := exec.CommandContext(ctx, bin, "--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	return parseNvidia(string(out)), nil
}

func parseNvidia(s string) []GPU {
	var out []GPU
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		f := strings.Split(line, ",")
		if len(f) != 5 {
			continue
		}
		num := func(i int) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(f[i]), 64); return v }
		out = append(out, GPU{Name: strings.TrimSpace(f[0]), Usage: num(1),
			MemoryUsed: uint64(num(2)) << 20, MemoryTotal: uint64(num(3)) << 20, Temperature: num(4)})
	}
	return out
}

func containers(ctx context.Context, dockerBin string) (Containers, error) {
	var cs Containers
	out, err := exec.CommandContext(ctx, dockerBin, "ps", "-a", "--format",
		`{{json .Names}}	{{json .State}}	{{json .Status}}	{{json (.Label "com.docker.compose.project")}}	{{json (.Label "com.docker.stack.namespace")}}`).Output()
	if err != nil {
		return cs, fmt.Errorf("docker ps: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 5 {
			continue
		}
		var c Container
		var swarm string
		_ = json.Unmarshal([]byte(f[0]), &c.Name)
		_ = json.Unmarshal([]byte(f[1]), &c.State)
		_ = json.Unmarshal([]byte(f[2]), &c.Status)
		_ = json.Unmarshal([]byte(f[3]), &c.Stack)
		_ = json.Unmarshal([]byte(f[4]), &swarm)
		if c.Stack == "" {
			c.Stack = swarm
		}
		if c.State == "running" {
			cs.Running++
		} else {
			cs.Stopped++
		}
		if strings.Contains(c.Status, "(unhealthy)") {
			cs.Unhealthy++
		}
		cs.List = append(cs.List, c)
	}
	if cs.Running > 0 {
		stats, err := exec.CommandContext(ctx, dockerBin, "stats", "--no-stream", "--format",
			`{{json .Name}}	{{json .CPUPerc}}	{{json .MemUsage}}	{{json .NetIO}}`).Output()
		if err != nil {
			return cs, fmt.Errorf("docker stats: %w", err)
		}
		byName := map[string][3]string{}
		sc := bufio.NewScanner(bytes.NewReader(stats))
		for sc.Scan() {
			f := strings.Split(sc.Text(), "\t")
			if len(f) != 4 {
				continue
			}
			var name string
			var v [3]string
			_ = json.Unmarshal([]byte(f[0]), &name)
			for i := range v {
				_ = json.Unmarshal([]byte(f[i+1]), &v[i])
			}
			byName[name] = v
		}
		for i, c := range cs.List {
			if v, ok := byName[c.Name]; ok {
				cs.List[i].CPU, cs.List[i].Memory, cs.List[i].NetIO = v[0], v[1], v[2]
			}
		}
	}
	sort.Slice(cs.List, func(i, j int) bool {
		a, b := cs.List[i], cs.List[j]
		if (a.State == "running") != (b.State == "running") {
			return b.State == "running" // stopped ones first: they need a look
		}
		if a.Stack != b.Stack {
			return a.Stack < b.Stack
		}
		return a.Name < b.Name
	})
	return cs, nil
}
