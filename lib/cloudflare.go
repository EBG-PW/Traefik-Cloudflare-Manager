package lib

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type CloudflareClient struct {
	token string
	http  *http.Client
}

type cfResponse[T any] struct {
	Success bool      `json:"success"`
	Errors  []cfError `json:"errors"`
	Result  T         `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type CloudflareZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cfDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

func NewCloudflareClient(token string) *CloudflareClient {
	return &CloudflareClient{token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *CloudflareClient) VerifyToken(ctx context.Context) error {
	var out cfResponse[map[string]any]
	return c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, &out)
}

func (c *CloudflareClient) FindZone(ctx context.Context, domain string) (CloudflareZone, error) {
	var out cfResponse[[]CloudflareZone]
	err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(domain), nil, &out)
	if err != nil {
		return CloudflareZone{}, err
	}
	if len(out.Result) == 0 {
		return CloudflareZone{}, fmt.Errorf("zone %q was not found; add the zone to Cloudflare first", domain)
	}
	return out.Result[0], nil
}

func (c *CloudflareClient) EnsureARecord(ctx context.Context, zoneID, name, ip string, proxied bool) (string, error) {
	var list cfResponse[[]cfDNSRecord]
	path := fmt.Sprintf("/zones/%s/dns_records?type=A&name=%s", url.PathEscape(zoneID), url.QueryEscape(name))
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return "", err
	}
	payload := map[string]any{"type": "A", "name": name, "content": ip, "ttl": 1, "proxied": proxied}
	var out cfResponse[cfDNSRecord]
	if len(list.Result) > 0 {
		id := list.Result[0].ID
		err := c.do(ctx, http.MethodPut, fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(zoneID), url.PathEscape(id)), payload, &out)
		return id, err
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(zoneID)), payload, &out); err != nil {
		return "", err
	}
	return out.Result.ID, nil
}

func (c *CloudflareClient) EnsureCNAMERecord(ctx context.Context, zoneID, name, target string, proxied bool) (string, error) {
	var list cfResponse[[]cfDNSRecord]
	path := fmt.Sprintf("/zones/%s/dns_records?name=%s", url.PathEscape(zoneID), url.QueryEscape(name))
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return "", err
	}

	cnameID := ""
	for _, record := range list.Result {
		if strings.EqualFold(record.Type, "CNAME") && cnameID == "" {
			cnameID = record.ID
			continue
		}
		if err := c.deleteDNSRecordByID(ctx, zoneID, record.ID); err != nil {
			return "", err
		}
	}

	payload := map[string]any{"type": "CNAME", "name": name, "content": target, "ttl": 1, "proxied": proxied}
	var out cfResponse[cfDNSRecord]
	if cnameID != "" {
		err := c.do(ctx, http.MethodPut, fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(zoneID), url.PathEscape(cnameID)), payload, &out)
		return cnameID, err
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(zoneID)), payload, &out); err != nil {
		return "", err
	}
	return out.Result.ID, nil
}

func (c *CloudflareClient) DeleteDNSRecord(ctx context.Context, zoneID, recordID, name string) error {
	if recordID == "" && name != "" {
		var list cfResponse[[]cfDNSRecord]
		path := fmt.Sprintf("/zones/%s/dns_records?name=%s", url.PathEscape(zoneID), url.QueryEscape(name))
		if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
			return err
		}
		for _, record := range list.Result {
			if err := c.deleteDNSRecordByID(ctx, zoneID, record.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if recordID == "" {
		return nil
	}
	return c.deleteDNSRecordByID(ctx, zoneID, recordID)
}

func (c *CloudflareClient) DeleteARecord(ctx context.Context, zoneID, recordID, name string) error {
	return c.DeleteDNSRecord(ctx, zoneID, recordID, name)
}

func (c *CloudflareClient) deleteDNSRecordByID(ctx context.Context, zoneID, recordID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", url.PathEscape(zoneID), url.PathEscape(recordID)), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare returned %s: %s", resp.Status, string(raw))
	}
	var out cfResponse[map[string]any]
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if !out.Success {
		return cfErrors(out.Errors)
	}
	return nil
}

func (c *CloudflareClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.cloudflare.com/client/v4"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare returned %s: %s", resp.Status, string(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	return responseOK(out)
}

func responseOK(out any) error {
	switch v := out.(type) {
	case *cfResponse[map[string]any]:
		if !v.Success {
			return cfErrors(v.Errors)
		}
	case *cfResponse[[]CloudflareZone]:
		if !v.Success {
			return cfErrors(v.Errors)
		}
	case *cfResponse[[]cfDNSRecord]:
		if !v.Success {
			return cfErrors(v.Errors)
		}
	case *cfResponse[cfDNSRecord]:
		if !v.Success {
			return cfErrors(v.Errors)
		}
	}
	return nil
}

func cfErrors(errs []cfError) error {
	if len(errs) == 0 {
		return errors.New("unknown Cloudflare API error")
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Message
	}
	return errors.New(strings.Join(parts, "; "))
}
