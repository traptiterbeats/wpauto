package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/traptiterbeats/wpauto/internal/cli"
	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/web"
)

var version = "dev"

const usage = `wpauto - automated WordPress installer for cPanel hosts

Usage:
  wpauto                 open the management menu
  wpauto serve           run the web panel (used by systemd)
  wpauto init [--port N] create config with random credentials
  wpauto info            show panel URL and status
  wpauto port N          change the panel port
  wpauto start|stop|restart
  wpauto update          install the latest release
  wpauto uninstall
  wpauto version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return cli.Menu(version)
	}
	switch args[0] {
	case "serve":
		c, err := config.Load()
		if err != nil {
			return err
		}
		s, err := web.New(c, version)
		if err != nil {
			return err
		}
		return s.ListenAndServe()
	case "init":
		port := 0
		if len(args) == 3 && args[1] == "--port" {
			p, err := strconv.Atoi(args[2])
			if err != nil {
				return fmt.Errorf("invalid port")
			}
			port = p
		}
		return cli.Init(port)
	case "info":
		return cli.Info(version)
	case "port":
		if len(args) != 2 {
			return fmt.Errorf("usage: wpauto port N")
		}
		c, err := config.Load()
		if err != nil {
			return err
		}
		return cli.ChangePort(c, args[1])
	case "start", "stop", "restart", "status":
		return execSystemctl(args[0])
	case "update":
		return cli.Update()
	case "uninstall":
		_, err := cli.Uninstall()
		return err
	case "version", "-v", "--version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	fmt.Print(usage)
	return fmt.Errorf("unknown command %q", args[0])
}
