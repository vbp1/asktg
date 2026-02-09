package security

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultFetchTimeout   = 10 * time.Second
	DefaultMaxSizeBytes   = 5 * 1024 * 1024
	DefaultMaxRedirects   = 5
	DefaultMaxURLsMessage = 5
)

func ValidateFetchURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("only http/https are allowed")
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("missing host")
	}

	lowerHost := strings.ToLower(host)
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".local") {
		return errors.New("localhost and .local hosts are blocked")
	}

	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("blocked host IP: %s", ip.String())
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("cannot resolve host: %w", err)
	}
	if len(ips) == 0 {
		return errors.New("host resolves to no IPs")
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("resolved blocked IP: %s", ip.String())
		}
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	addr, err := netip.ParseAddr(ip.String())
	if err != nil {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsLinkLocalMulticast() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() || addr.IsPrivate() {
		return true
	}
	if addr.Is4() {
		ip4 := addr.As4()
		if ip4[0] == 100 && ip4[1]&0xC0 == 64 {
			return true
		}
	}
	return false
}
