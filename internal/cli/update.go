package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ermos/minato/internal/update"
)

const (
	updateService = "/etc/systemd/system/minato-update.service"
	updateTimer   = "/etc/systemd/system/minato-update.timer"
)

const updateServiceUnit = `# Managed by minato update auto
[Unit]
Description=Update Minato to its latest release
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s update --yes --auto
`

const updateTimerUnit = `# Managed by minato update auto
[Unit]
Description=Daily Minato update

[Timer]
OnCalendar=*-*-* %s
RandomizedDelaySec=30m
Persistent=true

[Install]
WantedBy=timers.target
`

func updateCmd() *cobra.Command {
	var check, yes, auto, force bool
	var version string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update minato to the latest release",
		Long: `Checks the latest release on GitHub, verifies its checksum and replaces the
minato binary. On a server, the minato service is restarted afterwards.

Use "minato update auto on" to install a daily systemd timer that keeps the
server up to date. Automatic updates never cross a major version.`,
		Example: `  minato update --check          # only tell whether an update exists
  sudo minato update             # install the latest release
  sudo minato update --version v0.2.0
  sudo minato update auto on     # daily automatic updates`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			u := &update.Updater{}

			target := version
			if target == "" {
				latest, err := u.Latest(ctx)
				if err != nil {
					return err
				}
				target = latest
			}
			targetV, err := update.ParseVersion(target)
			if err != nil {
				return err
			}
			currentV, curErr := update.ParseVersion(Version)
			switch {
			case curErr != nil && !force:
				return fmt.Errorf("this build (%s) has no release version, use --force to replace it with %s", Version, target)
			case curErr == nil && version == "" && !currentV.Less(targetV) && !force:
				fmt.Printf("minato %s is up to date\n", Version)
				return nil
			case curErr == nil && auto && !currentV.SameMajor(targetV):
				fmt.Printf("minato %s is available, a new major version: update by hand with `sudo minato update`\n", target)
				return nil
			}

			if check {
				fmt.Printf("minato %s is available (current %s): sudo minato update\n", target, Version)
				return nil
			}
			if !yes && !confirm(fmt.Sprintf("Update minato %s to %s?", Version, target)) {
				return errors.New("aborted")
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			fmt.Printf("downloading minato %s (%s)\n", target, update.AssetName())
			bin, err := u.Download(ctx, target)
			if err != nil {
				return err
			}
			if err := update.Replace(exe, bin); err != nil {
				return err
			}
			fmt.Printf("✔ %s updated to %s\n", exe, target)
			return restartService()
		},
	}
	f := cmd.Flags()
	f.BoolVar(&check, "check", false, "only check whether an update is available")
	f.BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	f.StringVar(&version, "version", "", "install this version instead of the latest one")
	f.BoolVar(&force, "force", false, "replace the binary even if it is not older (or is a dev build)")
	f.BoolVar(&auto, "auto", false, "automatic run: skip new major versions")
	_ = f.MarkHidden("auto")
	cmd.AddCommand(updateAutoCmd())
	return cmd
}

func updateAutoCmd() *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "auto on|off|status",
		Short: "Enable or disable daily automatic updates (systemd timer)",
		Example: `  sudo minato update auto on            # every day around 05:00
  sudo minato update auto on --at 03:30
  sudo minato update auto off
  minato update auto status`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"on", "off", "status"},
		RunE: func(_ *cobra.Command, args []string) error {
			if runtime.GOOS != "linux" {
				return errors.New("automatic updates need systemd: on a laptop, run `minato update` from time to time")
			}
			switch args[0] {
			case "on":
				if _, err := time.Parse("15:04", at); err != nil {
					return fmt.Errorf("invalid --at %q, expected HH:MM", at)
				}
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				exe, _ = filepath.EvalSymlinks(exe)
				if err := os.WriteFile(updateService, []byte(fmt.Sprintf(updateServiceUnit, exe)), 0o644); err != nil {
					return permissionHint(err)
				}
				if err := os.WriteFile(updateTimer, []byte(fmt.Sprintf(updateTimerUnit, at+":00")), 0o644); err != nil {
					return permissionHint(err)
				}
				if err := systemctl("daemon-reload"); err != nil {
					return err
				}
				if err := systemctl("enable", "--now", "minato-update.timer"); err != nil {
					return err
				}
				fmt.Printf("✔ automatic updates on, every day around %s (same major version only)\n", at)
			case "off":
				_ = systemctl("disable", "--now", "minato-update.timer")
				for _, f := range []string{updateTimer, updateService} {
					if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
						return permissionHint(err)
					}
				}
				if err := systemctl("daemon-reload"); err != nil {
					return err
				}
				fmt.Println("✔ automatic updates off")
			case "status":
				out, _ := exec.Command("systemctl", "is-enabled", "minato-update.timer").Output()
				if strings.TrimSpace(string(out)) != "enabled" {
					fmt.Println("automatic updates: off")
					return nil
				}
				next, _ := exec.Command("systemctl", "show", "minato-update.timer", "--property=NextElapseUSecRealtime", "--value").Output()
				fmt.Printf("automatic updates: on, next check %s\n", strings.TrimSpace(string(next)))
			default:
				return fmt.Errorf("unknown argument %q, expected on, off or status", args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&at, "at", "05:00", "time of the daily check (HH:MM, a random delay of up to 30 minutes is added)")
	return cmd
}

// restartService restarts the minato daemon when it runs under systemd, so it
// uses the new binary.
func restartService() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	out, _ := exec.Command("systemctl", "is-active", "minato").Output()
	if strings.TrimSpace(string(out)) != "active" {
		return nil
	}
	if err := systemctl("restart", "minato"); err != nil {
		return err
	}
	fmt.Println("✔ minato service restarted")
	return nil
}

func systemctl(args ...string) error {
	if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func permissionHint(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w (run with sudo)", err)
	}
	return err
}
