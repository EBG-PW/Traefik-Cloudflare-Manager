package lib

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"
)

func Env(key, fallback string) string {
	if value := strings.TrimSpace(Getenv(key)); value != "" {
		return value
	}
	return fallback
}

var Getenv = os.Getenv

func CleanHost(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimSuffix(v, ".")
	if idx := strings.IndexByte(v, '/'); idx >= 0 {
		v = v[:idx]
	}
	return v
}

func ValidHost(v string) bool {
	if len(v) == 0 || len(v) > 253 || strings.Contains(v, "..") {
		return false
	}
	labels := strings.Split(v, ".")
	if len(labels) < 2 {
		return false
	}
	labelRE := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	for _, label := range labels {
		if !labelRE.MatchString(label) {
			return false
		}
	}
	return true
}

func ValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

func IsPrivateIP(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()
}

func FormatBytes(v uint64) string {
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := uint64(unit), 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(v)/float64(div), "KMGTPE"[exp])
}
