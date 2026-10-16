// Package usage 是所有应用内容流共用的唯一计量入口。
//
// 预算模型：
//   - usage_periods.sent/received 只保存“已确认”字节；
//   - usage_reservations 保存活动响应“已预留但未确认”的字节（reserved-flushed）；
//   - 配额检查 = 已确认 + 全部未确认预留 + 新预留，因此并发请求不能重复得到同一额度；
//   - 进程崩溃后遗留的 open 预留转为 orphaned，未确认部分继续保守占用，直到管理员对账。
//
// 在途缓冲误差：字节在 Write 返回后才确认，内核/网络层已接收但未必送达的数据
// 不可测；单个响应最多比实际送达多记一个写入块，且永远不会少记已预留的预算。
package usage

import (
	"database/sql"
	"errors"
	"sync"
	"time"

	"filepanel/internal/store"
)

const (
	CategoryPublic = "public_download"
	CategoryAdmin  = "admin_download"
	CategoryAsset  = "asset"
	CategoryUpload = "admin_upload"
)

const DefaultBlockSize int64 = 1 << 20

var (
	ErrQuota   = errors.New("本机流量已达保护线")
	ErrIPLimit = errors.New("当前来源的流量已达本周期上限")
	ErrBusy    = errors.New("当前来源的并发下载过多")
	ErrBudget  = errors.New("响应超出预计字节数")
)

type Period struct {
	Start     int64 `json:"start"`
	End       int64 `json:"end"`
	Sent      int64 `json:"sent"`
	Received  int64 `json:"received"`
	Downloads int64 `json:"downloads"`
}

type Meter struct {
	db        *store.Store
	now       func() time.Time
	BlockSize int64
	mu        sync.Mutex
	active    map[string]int
}

func New(s *store.Store, now func() time.Time) (*Meter, error) {
	if now == nil {
		now = time.Now
	}
	m := &Meter{db: s, now: now, BlockSize: DefaultBlockSize, active: map[string]int{}}
	// 新进程不可能还有活动响应：把遗留 open 预留标记为 orphaned，未确认部分继续占用额度。
	if _, err := s.DB.Exec("UPDATE usage_reservations SET state='orphaned',updated_at=? WHERE state='open'", now().Unix()); err != nil {
		return nil, err
	}
	return m, nil
}

// period 返回当前周期。时钟回拨或重置日调整都不会创建更早的周期，因此不会重复发放额度。
func (m *Meter) period(tx *sql.Tx, cfg Config) (Period, error) {
	now := m.now().UTC()
	var p Period
	err := tx.QueryRow("SELECT start,end,sent,received,downloads FROM usage_periods ORDER BY start DESC LIMIT 1").Scan(&p.Start, &p.End, &p.Sent, &p.Received, &p.Downloads)
	hasLatest := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if hasLatest && now.Unix() < p.End {
		return p, nil
	}
	start := PeriodStart(now, cfg.ResetDay).Unix()
	if hasLatest && start < p.End {
		start = p.End
	}
	end := NextBoundary(time.Unix(start, 0), cfg.ResetDay).Unix()
	if _, err := tx.Exec("INSERT INTO usage_periods(start,end,created_at) VALUES(?,?,?)", start, end, now.Unix()); err != nil {
		return p, err
	}
	return Period{Start: start, End: end}, nil
}

func (m *Meter) pending(tx *sql.Tx, periodStart int64, ip string) (all, orphaned, forIP int64, err error) {
	err = tx.QueryRow(`SELECT COALESCE(SUM(reserved-flushed),0),
		COALESCE(SUM(CASE WHEN state='orphaned' THEN reserved-flushed ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ip=? THEN reserved-flushed ELSE 0 END),0)
		FROM usage_reservations WHERE period_start=?`, ip, periodStart).Scan(&all, &orphaned, &forIP)
	return
}

func usedBytes(cfg Config, p Period, pending int64) int64 {
	used := p.Sent + pending
	if cfg.Basis == BasisTotal {
		used += p.Received
	}
	return used
}

// headroom 计算总量与来源两个维度的剩余字节；limited=false 表示不受限。
func (m *Meter) headroom(tx *sql.Tx, cfg Config, p Period, ip string) (grant int64, limited bool, err error) {
	if !cfg.Protect || cfg.QuotaBytes <= 0 {
		return 0, false, nil
	}
	all, _, forIP, err := m.pending(tx, p.Start, ip)
	if err != nil {
		return 0, false, err
	}
	room := cfg.Limit() - usedBytes(cfg, p, all)
	if room <= 0 {
		return 0, true, ErrQuota
	}
	var ipSent int64
	if err := tx.QueryRow("SELECT COALESCE((SELECT bytes FROM usage_ips WHERE period_start=? AND ip=?),0)", p.Start, ip).Scan(&ipSent); err != nil {
		return 0, false, err
	}
	ipRoom := cfg.ipLimit() - ipSent - forIP
	if ipRoom <= 0 {
		return 0, true, ErrIPLimit
	}
	if ipRoom < room {
		room = ipRoom
	}
	return room, true, nil
}

// Available 是授权时的检查：不占用并发名额，也不预留字节。
func (m *Meter) Available(ip string) error {
	cfg, err := m.Config()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.Protect && m.active[ip] >= cfg.IPConcurrency {
		return ErrBusy
	}
	return m.checkLocked(cfg, ip)
}

func (m *Meter) checkLocked(cfg Config, ip string) error {
	tx, err := m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := m.period(tx, cfg)
	if err != nil {
		return err
	}
	if _, _, err := m.headroom(tx, cfg, p, ip); err != nil {
		return err
	}
	return tx.Commit()
}

// Begin 为一个响应占用并发名额并检查额度。expect 是计划发送的字节数。
func (m *Meter) Begin(ip, category string, expect int64) (*Stream, error) {
	cfg, err := m.Config()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.Protect && m.active[ip] >= cfg.IPConcurrency {
		return nil, ErrBusy
	}
	if expect > 0 {
		if err := m.checkLocked(cfg, ip); err != nil {
			return nil, err
		}
	}
	m.active[ip]++
	return &Stream{m: m, ip: ip, category: category, expect: expect}, nil
}

// RecordReceived 记录入站字节（上传）。入站不会被拒绝，只进入周期与分类统计。
func (m *Meter) RecordReceived(category string, n int64) error {
	if n <= 0 {
		return nil
	}
	cfg, err := m.Config()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := m.period(tx, cfg)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE usage_periods SET received=received+? WHERE start=?", n, p.Start); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO usage_categories(period_start,category,direction,bytes) VALUES(?,?,'received',?)
		ON CONFLICT(period_start,category,direction) DO UPDATE SET bytes=bytes+excluded.bytes`, p.Start, category, n); err != nil {
		return err
	}
	return tx.Commit()
}

// Reconcile 释放崩溃遗留的未确认预留。已确认字节不变，返回释放的字节数。
func (m *Meter) Reconcile() (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.db.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var released int64
	if err := tx.QueryRow("SELECT COALESCE(SUM(reserved-flushed),0) FROM usage_reservations WHERE state='orphaned'").Scan(&released); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("DELETE FROM usage_reservations WHERE state='orphaned'"); err != nil {
		return 0, err
	}
	return released, tx.Commit()
}

type CategoryUsage struct {
	Category  string `json:"category"`
	Direction string `json:"direction"`
	Bytes     int64  `json:"bytes"`
}

type Status struct {
	Config     Config          `json:"config"`
	State      string          `json:"state"`
	Period     Period          `json:"period"`
	Limit      int64           `json:"limit"`
	Used       int64           `json:"used"`
	Confirmed  int64           `json:"confirmed"`
	Pending    int64           `json:"pending"`
	Orphaned   int64           `json:"orphaned"`
	Active     int             `json:"active"`
	Categories []CategoryUsage `json:"categories"`
	History    []Period        `json:"history"`
}

const (
	StateOK           = "ok"
	StateDisabled     = "disabled"
	StateQuotaUnknown = "quota_unknown"
	StateExhausted    = "exhausted"
)

func (m *Meter) Status() (Status, error) {
	cfg, err := m.Config()
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.db.DB.Begin()
	if err != nil {
		return Status{}, err
	}
	defer tx.Rollback()
	p, err := m.period(tx, cfg)
	if err != nil {
		return Status{}, err
	}
	all, orphaned, _, err := m.pending(tx, p.Start, "")
	if err != nil {
		return Status{}, err
	}
	st := Status{Config: cfg, Period: p, Limit: cfg.Limit(), Pending: all, Orphaned: orphaned, Categories: []CategoryUsage{}, History: []Period{}}
	st.Confirmed = p.Sent
	if cfg.Basis == BasisTotal {
		st.Confirmed += p.Received
	}
	st.Used = usedBytes(cfg, p, all)
	switch {
	case !cfg.Protect:
		st.State = StateDisabled
	case cfg.QuotaBytes <= 0:
		st.State = StateQuotaUnknown
	case st.Used >= st.Limit:
		st.State = StateExhausted
	default:
		st.State = StateOK
	}
	for _, n := range m.active {
		st.Active += n
	}
	rows, err := tx.Query("SELECT category,direction,bytes FROM usage_categories WHERE period_start=? ORDER BY direction,category", p.Start)
	if err != nil {
		return Status{}, err
	}
	for rows.Next() {
		var c CategoryUsage
		if err := rows.Scan(&c.Category, &c.Direction, &c.Bytes); err != nil {
			rows.Close()
			return Status{}, err
		}
		st.Categories = append(st.Categories, c)
	}
	rows.Close()
	hist, err := tx.Query("SELECT start,end,sent,received,downloads FROM usage_periods ORDER BY start DESC LIMIT 12")
	if err != nil {
		return Status{}, err
	}
	for hist.Next() {
		var h Period
		if err := hist.Scan(&h.Start, &h.End, &h.Sent, &h.Received, &h.Downloads); err != nil {
			hist.Close()
			return Status{}, err
		}
		st.History = append(st.History, h)
	}
	hist.Close()
	return st, tx.Commit()
}
