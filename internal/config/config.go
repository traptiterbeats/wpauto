// Package config loads and saves the wpauto configuration file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
)

// Config is the on-disk configuration of the panel.
type Config struct {
	Port         int    `json:"port"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	BasePath     string `json:"base_path"`
	SessionKey   string `json:"session_key"`
	Domain       string `json:"domain,omitempty"`
	CertFile     string `json:"cert_file,omitempty"`
	KeyFile      string `json:"key_file,omitempty"`
	// AdminUI shows the admin pages (theme catalog: add/delete themes, demos, plugin library).
	// Missing means true; set "admin_ui": false to hide them and answer 403.
	AdminUI *bool `json:"admin_ui,omitempty"`
}

// AdminUIEnabled reports whether admin pages are on (default true).
func (c *Config) AdminUIEnabled() bool { return c == nil || c.AdminUI == nil || *c.AdminUI }

// Dir is the configuration directory. It can be overridden with WPAUTO_DIR (useful for tests).
func Dir() string {
	if d := os.Getenv("WPAUTO_DIR"); d != "" {
		return d
	}
	return "/etc/wpauto"
}

// Path is the configuration file path.
func Path() string { return filepath.Join(Dir(), "config.json") }

// Exists reports whether a configuration file is present.
func Exists() bool {
	_, err := os.Stat(Path())
	return err == nil
}

// Load reads the configuration file.
func Load() (*Config, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("wpauto is not initialised yet (run: wpauto init)")
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the configuration atomically with 0600 permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// SetPassword stores a bcrypt hash of the password.
func (c *Config) SetPassword(p string) error {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	c.PasswordHash = string(h)
	return nil
}

// CheckPassword compares a plain password with the stored hash.
func (c *Config) CheckPassword(p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(c.PasswordHash), []byte(p)) == nil
}

// TLSEnabled reports whether certificate files are configured.
func (c *Config) TLSEnabled() bool { return c.CertFile != "" && c.KeyFile != "" }

const alnum = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RandString returns a random alphanumeric string of length n.
func RandString(n int) string {
	out := make([]byte, n)
	for i := range out {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alnum))))
		out[i] = alnum[v.Int64()]
	}
	return string(out)
}

// RandHex returns n random bytes encoded as hex.
func RandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RandPort returns a random port in a high range.
func RandPort() int {
	v, _ := rand.Int(rand.Reader, big.NewInt(40000))
	return int(v.Int64()) + 20000
}

// New creates a fresh configuration with random credentials and returns the plain password.
func New(port int) (*Config, string, error) {
	if port <= 0 {
		port = RandPort()
	}
	pass := RandString(16)
	c := &Config{
		Port:       port,
		Username:   "admin_" + RandString(5),
		BasePath:   "/" + RandString(10) + "/",
		SessionKey: RandHex(32),
	}
	if err := c.SetPassword(pass); err != nil {
		return nil, "", err
	}
	return c, pass, nil
}
