package lib

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
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
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, "443"), &tls.Config{
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
