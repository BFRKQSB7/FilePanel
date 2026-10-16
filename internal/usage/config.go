package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const settingKey = "usage_config"

// 内部计量单位始终是字节；界面使用十进制 GB/TB 换算。
const (
	GB int64 = 1000 * 1000 * 1000
	TB int64 = 1000 * GB
)

const (
	BasisSent  = "sent"
	BasisTotal = "total"
)

// Config 是节点级保护设置。QuotaBytes 为 0 表示额度未知，此时总量保护不生效。
type Config struct {
	Protect        bool     `json:"protect"`
	QuotaBytes     int64    `json:"quota_bytes"`
	Basis          string   `json:"basis"`
	ResetDay       int      `json:"reset_day"`
	IPConcurrency  int      `json:"ip_concurrency"`
	IPPercent      int      `json:"ip_percent"`
	TrustedProxies []string `json:"trusted_proxies"`
}

func DefaultConfig() Config {
	return Config{Protect: true, Basis: BasisSent, ResetDay: 1, IPConcurrency: 3, IPPercent: 50, TrustedProxies: []string{}}
}

func (c Config) Validate() error {
	if c.QuotaBytes < 0 || c.QuotaBytes > 1000*TB {
		return errors.New("流量额度超出允许范围")
	}
	if c.Basis != BasisSent && c.Basis != BasisTotal {
		return errors.New("额度口径只能是发送或收发合计")
	}
	if c.ResetDay < 1 || c.ResetDay > 31 {
		return errors.New("重置日必须在 1 到 31 之间")
	}
	if c.IPConcurrency < 1 || c.IPConcurrency > 100 {
		return errors.New("单 IP 并发必须在 1 到 100 之间")
	}
	if c.IPPercent < 1 || c.IPPercent > 100 {
		return errors.New("单 IP 流量占比必须在 1% 到 100% 之间")
	}
	if len(c.TrustedProxies) > 32 {
		return errors.New("可信代理最多 32 项")
	}
	for _, item := range c.TrustedProxies {
		if _, err := ParseProxy(item); err != nil {
			return fmt.Errorf("可信代理 %q 无效", item)
		}
	}
	return nil
}

// ParseProxy 接受单个 IP 或 CIDR。
func ParseProxy(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "/") {
		return netip.ParsePrefix(value)
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()), nil
}

func (m *Meter) Config() (Config, error) {
	raw, ok, err := m.db.Setting(settingKey)
	if err != nil {
		return Config{}, err
	}
	cfg := DefaultConfig()
	if !ok {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, fmt.Errorf("读取保护设置失败: %w", err)
	}
	if cfg.TrustedProxies == nil {
		cfg.TrustedProxies = []string{}
	}
	return cfg, nil
}

func (m *Meter) SetConfig(cfg Config) error {
	if cfg.TrustedProxies == nil {
		cfg.TrustedProxies = []string{}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return m.db.SetSetting(settingKey, string(raw))
}

// Limit 是实际执行线：已配置额度预留 5%。额度未知返回 0。
func (c Config) Limit() int64 {
	if c.QuotaBytes <= 0 {
		return 0
	}
	return c.QuotaBytes - c.QuotaBytes/20
}

func (c Config) ipLimit() int64 {
	limit := c.Limit()
	if limit == 0 {
		return 0
	}
	value := limit * int64(c.IPPercent) / 100 // 额度上限 1000 TB，乘 100 不会溢出 int64
	if value < 1 {
		value = 1
	}
	return value
}

// PeriodStart 返回 now 所在周期的起点（UTC 0 点）。重置日大于当月天数时取月底。
func PeriodStart(now time.Time, resetDay int) time.Time {
	now = now.UTC()
	boundary := monthBoundary(now.Year(), now.Month(), resetDay)
	if boundary.After(now) {
		prev := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		return monthBoundary(prev.Year(), prev.Month(), resetDay)
	}
	return boundary
}

// NextBoundary 返回严格晚于 t 的第一个周期边界。
func NextBoundary(t time.Time, resetDay int) time.Time {
	t = t.UTC()
	boundary := monthBoundary(t.Year(), t.Month(), resetDay)
	if boundary.After(t) {
		return boundary
	}
	next := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return monthBoundary(next.Year(), next.Month(), resetDay)
}

func monthBoundary(year int, month time.Month, resetDay int) time.Time {
	days := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if resetDay > days {
		resetDay = days
	}
	return time.Date(year, month, resetDay, 0, 0, 0, 0, time.UTC)
}
