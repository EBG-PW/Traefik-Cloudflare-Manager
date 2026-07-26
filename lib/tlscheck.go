package lib

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TLSCertificateInfo struct {
	NotBefore time.Time
	NotAfter  time.Time
	Issuer    string
	Subject   string
}

func CheckHTTPSCertificate(ctx context.Context, host string) (TLSCertificateInfo, error) {
	return CheckHTTPSCertificateAt(ctx, net.JoinHostPort(host, "443"), host)
}

func CheckHTTPSCertificateAt(ctx context.Context, address, host string) (TLSCertificateInfo, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return TLSCertificateInfo{}, err
	}
	defer conn.Close()
	select {
	case <-ctx.Done():
		return TLSCertificateInfo{}, ctx.Err()
	default:
	}
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return TLSCertificateInfo{}, fmt.Errorf("no certificate was served")
	}
	cert := state.PeerCertificates[0]
	if err := cert.VerifyHostname(host); err != nil {
		return TLSCertificateInfo{}, err
	}
	if cert.NotAfter.Before(time.Now()) {
		return TLSCertificateInfo{}, fmt.Errorf("certificate is expired")
	}
	if cert.CheckSignatureFrom(cert) == nil && strings.EqualFold(cert.Issuer.String(), cert.Subject.String()) {
		return TLSCertificateInfo{}, fmt.Errorf("self-signed certificate is still being served")
	}
	return TLSCertificateInfo{
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		Issuer:    cert.Issuer.String(),
		Subject:   cert.Subject.String(),
	}, nil
}

func ReadACMECertificate(dataDir, host string) (TLSCertificateInfo, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "traefik", "acme.json"))
	if err != nil {
		return TLSCertificateInfo{}, err
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return TLSCertificateInfo{}, err
	}
	certificate, err := findACMECertificate(document, CleanHost(host))
	if err != nil {
		return TLSCertificateInfo{}, err
	}
	return certificateInfo(certificate, host)
}

func findACMECertificate(value any, host string) (*x509.Certificate, error) {
	switch typed := value.(type) {
	case map[string]any:
		if domain, ok := typed["domain"].(map[string]any); ok {
			main, _ := domain["main"].(string)
			matches := strings.EqualFold(CleanHost(main), host)
			if !matches {
				if sans, ok := domain["sans"].([]any); ok {
					for _, san := range sans {
						if name, ok := san.(string); ok && strings.EqualFold(CleanHost(name), host) {
							matches = true
							break
						}
					}
				}
			}
			if matches {
				encoded, _ := typed["certificate"].(string)
				cert, err := decodeACMECertificate(encoded)
				if err == nil {
					return cert, nil
				}
			}
		}
		for _, child := range typed {
			if cert, err := findACMECertificate(child, host); err == nil {
				return cert, nil
			}
		}
	case []any:
		for _, child := range typed {
			if cert, err := findACMECertificate(child, host); err == nil {
				return cert, nil
			}
		}
	}
	return nil, fmt.Errorf("certificate for %s was not found in acme.json", host)
}

func decodeACMECertificate(encoded string) (*x509.Certificate, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if block, _ := pem.Decode(raw); block != nil {
		raw = block.Bytes
	}
	return x509.ParseCertificate(raw)
}

func certificateInfo(cert *x509.Certificate, host string) (TLSCertificateInfo, error) {
	if err := cert.VerifyHostname(host); err != nil {
		return TLSCertificateInfo{}, err
	}
	if cert.NotAfter.Before(time.Now()) {
		return TLSCertificateInfo{}, fmt.Errorf("certificate is expired")
	}
	return TLSCertificateInfo{
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		Issuer:    cert.Issuer.String(),
		Subject:   cert.Subject.String(),
	}, nil
}

func CheckHTTPSReady(ctx context.Context, host string) error {
	_, err := CheckHTTPSCertificate(ctx, host)
	return err
}

func WaitForHTTPSCertificate(ctx context.Context, host string, interval time.Duration) (TLSCertificateInfo, error) {
	var last error
	for {
		info, err := CheckHTTPSCertificate(ctx, host)
		if err == nil {
			return info, nil
		} else {
			last = err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if last != nil {
				return TLSCertificateInfo{}, last
			}
			return TLSCertificateInfo{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func WaitForHTTPSReady(ctx context.Context, host string, interval time.Duration) error {
	_, err := WaitForHTTPSCertificate(ctx, host, interval)
	return err
}
