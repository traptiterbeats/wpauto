// Package sslutil creates self-signed certificates and obtains Let's Encrypt ones via certbot.
package sslutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// SelfSigned writes a self-signed certificate valid for 10 years into dir.
func SelfSigned(dir, host string) (certFile, keyFile string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "wpauto"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else if host != "" {
		tpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
}

// LetsEncrypt obtains a certificate with certbot in standalone mode (needs port 80 free and the
// domain's A record pointing at this server). It installs certbot with apt when missing and adds a
// deploy hook that restarts wpauto after each renewal.
func LetsEncrypt(domain, email string) (certFile, keyFile string, err error) {
	if _, err := exec.LookPath("certbot"); err != nil {
		fmt.Println("Installing certbot ...")
		if err := run("apt-get", "update"); err != nil {
			return "", "", err
		}
		if err := run("apt-get", "install", "-y", "certbot"); err != nil {
			return "", "", err
		}
	}
	args := []string{"certonly", "--standalone", "--non-interactive", "--agree-tos", "-d", domain}
	if email != "" {
		args = append(args, "-m", email)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}
	if err := run("certbot", args...); err != nil {
		return "", "", fmt.Errorf("certbot failed (is port 80 free and does %s point to this server?): %w", domain, err)
	}
	hookDir := "/etc/letsencrypt/renewal-hooks/deploy"
	_ = os.MkdirAll(hookDir, 0o755)
	_ = os.WriteFile(filepath.Join(hookDir, "wpauto.sh"), []byte("#!/bin/sh\nsystemctl restart wpauto\n"), 0o755)
	live := filepath.Join("/etc/letsencrypt/live", domain)
	return filepath.Join(live, "fullchain.pem"), filepath.Join(live, "privkey.pem"), nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
