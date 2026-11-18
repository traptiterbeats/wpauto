package main

import (
	"os"
	"os/exec"
)

func execSystemctl(action string) error {
	cmd := exec.Command("systemctl", action, "wpauto")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
