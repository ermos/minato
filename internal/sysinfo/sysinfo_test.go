package sysinfo

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestHostParsers(t *testing.T) {
	// Two samples of /proc/stat: 100 jiffies passed, 25 of them busy (iowait counts as idle).
	a := parseCPUStat("cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 1 2 3 4 5\n")
	b := parseCPUStat("cpu  120 0 55 870 55 0 0 0 0 0\n")
	if got := cpuUsage(a, b); math.Abs(got-25) > 0.01 {
		t.Errorf("cpu usage = %.2f, want 25", got)
	}

	m := parseMeminfo("MemTotal:        8000000 kB\nMemFree:  1 kB\nMemAvailable:    6000000 kB\nSwapTotal: 2000000 kB\nSwapFree: 1500000 kB\n")
	if m.Total != 8000000*1024 || m.Available != 6000000*1024 || m.SwapTotal-m.SwapFree != 500000*1024 {
		t.Errorf("meminfo = %+v", m)
	}

	const dev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: %d 1 0 0 0 0 0 0 %d 1 0 0 0 0 0 0
  gre0: 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0
  eth0: %d 1 0 0 0 0 0 0 %d 1 0 0 0 0 0 0
docker0: %d 1 0 0 0 0 0 0 %d 1 0 0 0 0 0 0
vethab12: %d 1 0 0 0 0 0 0 %d 1 0 0 0 0 0 0
`
	sample := func(rx, tx int) map[string][2]uint64 {
		return parseNetDev(fmt.Sprintf(dev, 5, 5, rx, tx, 9, 9, 9, 9))
	}
	nets := netRates(sample(1000, 500), sample(3000, 1500), 2*time.Second)
	if len(nets) != 1 || nets[0].Name != "eth0" {
		t.Fatalf("interfaces = %+v, want eth0 only (no loopback, docker nor unused ones)", nets)
	}
	if n := nets[0]; n.RxRate != 1000 || n.TxRate != 500 || n.RxTotal != 3000 {
		t.Errorf("eth0 = %+v", n)
	}

	gpus := parseNvidia("NVIDIA L4, 37, 2048, 23034, 51\n")
	if len(gpus) != 1 || gpus[0].Usage != 37 || gpus[0].MemoryUsed != 2048<<20 || gpus[0].Temperature != 51 {
		t.Errorf("gpus = %+v", gpus)
	}
}
