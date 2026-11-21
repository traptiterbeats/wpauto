// Package cli implements the terminal management menu and its commands.
package cli

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/sslutil"
	"github.com/traptiterbeats/wpauto/internal/updater"
)

const (
	Service  = "wpauto"
	BinPath  = "/usr/local/bin/wpauto"
	UnitPath = "/etc/systemd/system/wpauto.service"
	cReset   = "\033[0m"
	cGreen   = "\033[32m"
	cRed     = "\033[31m"
	cYellow  = "\033[33m"
	cCyan    = "\033[36m"
	cBold    = "\033[1m"
)

var in = bufio.NewReader(os.Stdin)

func ask(prompt string) string {
	fmt.Print(prompt)
	s, _ := in.ReadString('\n')
	return strings.TrimSpace(s)
}

func confirm(prompt string) bool {
	a := strings.ToLower(ask(prompt + " [y/N]: "))
	return a == "y" || a == "yes"
}

// Init creates the configuration (first install) and prints the credentials once.
func Init(port int) error {
	if config.Exists() {
		fmt.Println("Config already exists, keeping it.")
		return nil
	}
	c, pass, err := config.New(port)
	if err != nil {
		return err
	}
	if err := c.Save(); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(cGreen + cBold + "wpauto installed." + cReset)
	fmt.Printf("  Panel:    %s\n", PanelURL(c))
	fmt.Printf("  Username: %s\n", c.Username)
	fmt.Printf("  Password: %s\n", pass)
	fmt.Println(cYellow + "Save the password now; it is stored only as a hash." + cReset)
	return nil
}

// PanelURL returns the URL the panel is reachable at.
func PanelURL(c *config.Config) string {
	scheme := "http"
	if c.TLSEnabled() {
		scheme = "https"
	}
	host := c.Domain
	if host == "" {
		host = PublicIP()
	}
	return fmt.Sprintf("%s://%s:%d%s", scheme, host, c.Port, c.BasePath)
}

// PublicIP finds the server's public IPv4, falling back to the first local address.
func PublicIP() string {
	cl := &http.Client{Timeout: 3 * time.Second}
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip"} {
		if r, err := cl.Get(u); err == nil {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 64))
			r.Body.Close()
			if ip := net.ParseIP(strings.TrimSpace(string(b))); ip != nil {
				return ip.String()
			}
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return "SERVER_IP"
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// Status returns "running" or "stopped".
func Status() string {
	out, _ := exec.Command("systemctl", "is-active", Service).Output()
	if strings.TrimSpace(string(out)) == "active" {
		return cGreen + "running" + cReset
	}
	return cRed + "stopped" + cReset
}

func openFirewall(port int) {
	if _, err := exec.LookPath("ufw"); err != nil {
		return
	}
	out, _ := exec.Command("ufw", "status").Output()
	if strings.Contains(string(out), "Status: active") {
		_ = exec.Command("ufw", "allow", strconv.Itoa(port)+"/tcp").Run()
	}
}

// Info prints the panel address and status.
func Info(version string) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Printf("%swpauto %s%s  status: %s\n", cBold, version, cReset, Status())
	fmt.Printf("Panel: %s%s%s\n", cCyan, PanelURL(c), cReset)
	fmt.Printf("User:  %s\n", c.Username)
	return nil
}

// Menu runs the interactive management menu.
func Menu(version string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("please run as root (sudo wpauto)")
	}
	for {
		c, err := config.Load()
		if err != nil {
			return err
		}
		fmt.Print("\033[H\033[2J")
		line := cCyan + strings.Repeat("═", 46) + cReset
		fmt.Println(line)
		fmt.Printf("%s   wpauto %s%s%s — management menu\n", cBold, cGreen, version, cReset+cBold)
		fmt.Print(cReset)
		fmt.Println(line)
		fmt.Printf("  %-8s %s%s%s\n", "Panel:", cCyan, PanelURL(c), cReset)
		fmt.Printf("  %-8s %s\n", "Status:", Status())
		fmt.Printf("  %-8s %s\n", "User:", c.Username)
		fmt.Println(line)
		menuSection("Panel")
		menuItem("1", "Change username / password")
		menuItem("2", "Change port")
		menuItem("3", "SSL settings")
		menuSection("Service")
		menuItem("4", "Start")
		menuItem("5", "Stop")
		menuItem("6", "Restart")
		menuItem("7", "Show logs")
		menuSection("Program")
		menuItem("8", "Update")
		menuItem("9", "Uninstall")
		fmt.Println()
		fmt.Printf("  %s[0]%s Exit\n", cRed+cBold, cReset)
		fmt.Println(line)
		choice := ask("\n" + cBold + "Select an option: " + cReset)
		switch choice {
		case "1":
			err = ChangeCredentials(c)
		case "2":
			err = ChangePort(c, ask("New port (1-65535): "))
		case "3":
			err = SSLMenu(c)
		case "4":
			err = systemctl("start", Service)
		case "5":
			err = systemctl("stop", Service)
		case "6":
			err = systemctl("restart", Service)
		case "7":
			cmd := exec.Command("journalctl", "-u", Service, "-n", "50", "--no-pager")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			err = cmd.Run()
		case "8":
			err = Update(false)
		case "9":
			if done, uerr := Uninstall(); uerr != nil {
				err = uerr
			} else if done {
				return nil
			}
		case "0", "q", "":
			return nil
		default:
			err = fmt.Errorf("unknown option")
		}
		if err != nil {
			fmt.Println(cRed+"  ✘ Error:"+cReset, err)
		} else {
			fmt.Println(cGreen + "  ✔ Done." + cReset)
		}
		ask("\nPress Enter to continue...")
	}
}

func menuSection(name string) {
	fmt.Printf("\n  %s%s%s\n", cYellow+cBold, name, cReset)
}

func menuItem(key, label string) {
	fmt.Printf("  %s[%s]%s %s\n", cCyan+cBold, key, cReset, label)
}

// ChangeCredentials sets a new username and password.
func ChangeCredentials(c *config.Config) error {
	u := ask(fmt.Sprintf("New username [%s]: ", c.Username))
	if u != "" {
		c.Username = u
	}
	p := ask("New password (empty = random): ")
	if p == "" {
		p = config.RandString(16)
		fmt.Println("Generated password:", cYellow+p+cReset)
	} else if len(p) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if err := c.SetPassword(p); err != nil {
		return err
	}
	if err := c.Save(); err != nil {
		return err
	}
	return systemctl("restart", Service)
}

// ChangePort validates and applies a new port.
func ChangePort(c *config.Config, s string) error {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("invalid port")
	}
	if p != c.Port {
		if l, err := net.Listen("tcp", ":"+strconv.Itoa(p)); err != nil {
			return fmt.Errorf("port %d is in use", p)
		} else {
			l.Close()
		}
	}
	c.Port = p
	if err := c.Save(); err != nil {
		return err
	}
	openFirewall(p)
	return systemctl("restart", Service)
}

// SSLMenu configures HTTPS for the panel.
func SSLMenu(c *config.Config) error {
	menuSection("SSL")
	menuItem("1", "Let's Encrypt certificate (needs a domain pointing to this server, port 80 free)")
	menuItem("2", "Self-signed certificate (browser will warn)")
	menuItem("3", "Disable SSL")
	fmt.Printf("  %s[0]%s Back\n", cRed+cBold, cReset)
	switch ask("\n" + cBold + "Select an option: " + cReset) {
	case "0", "":
		return nil
	case "1":
		d := ask("Domain (e.g. panel.example.com): ")
		if d == "" {
			return fmt.Errorf("domain is required")
		}
		email := ask("Email for expiry notices (optional): ")
		cert, key, err := sslutil.LetsEncrypt(d, email)
		if err != nil {
			return err
		}
		c.Domain, c.CertFile, c.KeyFile = d, cert, key
	case "2":
		host := c.Domain
		if host == "" {
			host = PublicIP()
		}
		cert, key, err := sslutil.SelfSigned(config.Dir(), host)
		if err != nil {
			return err
		}
		c.CertFile, c.KeyFile = cert, key
	case "3":
		c.CertFile, c.KeyFile = "", ""
	default:
		return nil
	}
	if err := c.Save(); err != nil {
		return err
	}
	if err := systemctl("restart", Service); err != nil {
		return err
	}
	fmt.Println("Panel:", PanelURL(c))
	return nil
}

// Update installs the latest stable release (or, with beta, the newest release including
// pre-releases) and restarts the service.
func Update(beta bool) error {
	start := time.Now()
	if beta {
		fmt.Println("[1/3] Checking newest release (beta channel, pre-releases included) ...")
	} else {
		fmt.Println("[1/3] Checking latest stable release ...")
	}
	updater.Progress = os.Stdout
	tag, err := updater.UpdateChannel(BinPath, beta)
	updater.Progress = nil
	if err != nil {
		return err
	}
	fmt.Println("[2/3] Restarting the panel service ...")
	if err := systemctl("restart", Service); err != nil {
		return err
	}
	fmt.Printf("[3/3] ✔ wpauto %s installed and running (%s)\n", tag, time.Since(start).Round(time.Second))
	return nil
}

// Uninstall removes the service, binary and (optionally) the configuration.
func Uninstall() (bool, error) {
	if !confirm("Remove wpauto from this server?") {
		return false, nil
	}
	_ = systemctl("disable", "--now", Service)
	_ = os.Remove(UnitPath)
	_ = systemctl("daemon-reload")
	if confirm("Also delete configuration and data in " + config.Dir() + "?") {
		_ = os.RemoveAll(config.Dir())
		_ = os.RemoveAll("/var/lib/wpauto")
	}
	if err := os.Remove(BinPath); err != nil && !os.IsNotExist(err) {
		return true, err
	}
	fmt.Println("wpauto removed.")
	return true, nil
}
