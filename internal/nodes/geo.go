package nodes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Only the configured node's observed public IP is sent to this provider.
// https://ipwhois.io/documentation documents HTTPS and selected response fields.
func lookupGeo(ctx context.Context, ip string) (string, error) {
	a, e := netip.ParseAddr(ip)
	if e != nil || !a.IsGlobalUnicast() || a.IsPrivate() {
		return "", ErrDenied
	}
	req, e := http.NewRequestWithContext(ctx, "GET", "https://ipwho.is/"+a.Unmap().String()+"?fields=success,country,region,city", nil)
	if e != nil {
		return "", e
	}
	c := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := c.Do(req)
	if e != nil {
		return "", e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", ErrDenied
	}
	var out struct {
		Success bool   `json:"success"`
		Country string `json:"country"`
		Region  string `json:"region"`
		City    string `json:"city"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&out) != nil || !out.Success {
		return "", ErrDenied
	}
	region := strings.TrimSpace(strings.Join([]string{out.Country, out.Region, out.City}, " "))
	if len(region) > 200 {
		return "", ErrDenied
	}
	return region, nil
}
func (p *Panel) RefreshRegion(ctx context.Context, id string) error {
	n, e := p.Get(id)
	if e != nil || n.ManualRegion {
		return e
	}
	ip, e := netip.ParseAddr(n.PublicIP)
	if e != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return nil
	}
	var region string
	var checked int64
	e = p.DB.DB.QueryRow("SELECT region,checked FROM node_geo_cache WHERE ip=?", ip.Unmap().String()).Scan(&region, &checked)
	if e != nil || p.Now().Unix() >= checked+86400 {
		lookup := p.GeoLookup
		if lookup == nil {
			lookup = lookupGeo
		}
		step, cancel := context.WithTimeout(ctx, 2*time.Second)
		region, e = lookup(step, ip.Unmap().String())
		cancel()
		if e != nil {
			region = ""
		}
		if _, e = p.DB.DB.Exec("INSERT INTO node_geo_cache(ip,region,checked) VALUES(?,?,?) ON CONFLICT(ip) DO UPDATE SET region=excluded.region,checked=excluded.checked", ip.Unmap().String(), region, p.Now().Unix()); e != nil {
			return e
		}
	}
	_, e = p.DB.DB.Exec("UPDATE nodes SET region=? WHERE id=? AND manual_region=0 AND public_ip=?", region, id, n.PublicIP)
	return e
}
