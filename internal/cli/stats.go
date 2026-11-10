package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ermos/minato/internal/sysinfo"
)

func statsCmd(g *globals) *cobra.Command {
	var asJSON bool
	var watch time.Duration
	cmd := &cobra.Command{
		Use:     "stats",
		Aliases: []string{"top"},
		Short:   "Show the state of the server: CPU, memory, disks, network, GPUs and containers",
		Example: `  minato stats
  minato stats --watch 5s
  minato stats --json | jq .containers.unhealthy`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client()
			if err != nil {
				return err
			}
			for {
				r, err := c.System()
				if err != nil {
					return err
				}
				if asJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(r)
				}
				if watch > 0 {
					fmt.Print("\033[H\033[2J")
				}
				printStats(r)
				if watch <= 0 {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(watch):
				}
			}
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	cmd.Flags().DurationVarP(&watch, "watch", "w", 0, "refresh every interval (for instance 5s)")
	return cmd
}

func printStats(r *sysinfo.Report) {
	h := r.Host
	head := []string{h.Name}
	if h.OS != "" {
		head = append(head, h.OS)
	}
	if h.Uptime > 0 {
		head = append(head, "up "+ago(time.Now().Add(-time.Duration(h.Uptime)*time.Second)))
	}
	fmt.Println(strings.Join(head, "   "))
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "CPU\t%s\t%.0f%% of %d cores, load %.2f %.2f %.2f\n", bar(r.CPU.Usage), r.CPU.Usage, r.CPU.Cores, h.Load[0], h.Load[1], h.Load[2])
	if m := r.Memory; m.Total > 0 {
		used := m.Total - m.Available
		fmt.Fprintf(w, "MEMORY\t%s\t%s / %s\n", bar(pct(used, m.Total)), bytesIEC(used), bytesIEC(m.Total))
		if m.SwapTotal > 0 {
			su := m.SwapTotal - m.SwapFree
			fmt.Fprintf(w, "SWAP\t%s\t%s / %s\n", bar(pct(su, m.SwapTotal)), bytesIEC(su), bytesIEC(m.SwapTotal))
		}
	}
	for _, d := range r.Disks {
		used := d.Total - d.Free
		fmt.Fprintf(w, "DISK\t%s\t%s / %s  %s\n", bar(pct(used, d.Total)), bytesIEC(used), bytesIEC(d.Total), d.Path)
	}
	for _, g := range r.GPUs {
		fmt.Fprintf(w, "GPU\t%s\t%.0f%%, %s / %s, %.0f°C  %s\n", bar(g.Usage), g.Usage, bytesIEC(g.MemoryUsed), bytesIEC(g.MemoryTotal), g.Temperature, g.Name)
	}
	if len(r.GPUs) == 0 {
		fmt.Fprintf(w, "GPU\tnone\t\n")
	}
	for _, n := range r.Network {
		fmt.Fprintf(w, "NET\t%s\t↓ %s/s  ↑ %s/s  (since boot ↓ %s ↑ %s)\n", n.Name, bytesSI(n.RxRate), bytesSI(n.TxRate), bytesSI(float64(n.RxTotal)), bytesSI(float64(n.TxTotal)))
	}
	_ = w.Flush()

	cs := r.Containers
	fmt.Printf("\nCONTAINERS  %d running, %d stopped, %d unhealthy\n", cs.Running, cs.Stopped, cs.Unhealthy)
	if len(cs.List) > 0 {
		w = tabwriter.NewWriter(os.Stdout, 0, 2, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTACK\tSTATUS\tCPU\tMEMORY\tNET I/O")
		for _, c := range cs.List {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Name, dash(c.Stack), c.Status, dash(c.CPU), dash(c.Memory), dash(c.NetIO))
		}
		_ = w.Flush()
	}
	for _, e := range r.Errors {
		fmt.Printf("\n! %s", e)
	}
	if len(r.Errors) > 0 {
		fmt.Println()
	}
}

func pct(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(part) / float64(total)
}

// bar draws a 20 cells gauge of a percentage.
func bar(p float64) string {
	n := int(p/5 + 0.5)
	n = max(0, min(20, n))
	return "[" + strings.Repeat("█", n) + strings.Repeat("·", 20-n) + "]"
}

func bytesIEC(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func bytesSI(n float64) string {
	for _, u := range []string{"B", "kB", "MB", "GB", "TB"} {
		if n < 1000 {
			if u == "B" {
				return fmt.Sprintf("%.0f %s", n, u)
			}
			return fmt.Sprintf("%.1f %s", n, u)
		}
		n /= 1000
	}
	return fmt.Sprintf("%.1f PB", n)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
