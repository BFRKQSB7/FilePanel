package usage

import (
	"errors"
	"sync"
	"testing"
	"time"

	"filepanel/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func setup(t *testing.T, cfg Config) (*store.Store, *Meter, *clock) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	clk := &clock{t: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)}
	m, err := New(s, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	m.BlockSize = 1000
	if err := m.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return s, m, clk
}

func smallConfig(quota int64) Config {
	c := DefaultConfig()
	c.QuotaBytes = quota
	c.IPConcurrency = 50
	c.IPPercent = 100
	return c
}

// drain 反复申请并确认，直到到线，返回确认的总字节数。
func drain(t *testing.T, m *Meter, ip string, want int64) int64 {
	t.Helper()
	st, err := m.Begin(ip, CategoryPublic, want)
	if err != nil {
		return 0
	}
	var got int64
	for got < want {
		n, err := st.Grant(300)
		if err != nil || n == 0 {
			break
		}
		st.Confirm(n)
		got += int64(n)
	}
	if err := st.Close(got == want); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestConcurrentStreamsCannotExceedProtectionLine(t *testing.T) {
	cfg := smallConfig(100_000) // 实际执行线 95_000
	_, m, _ := setup(t, cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var total int64
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := drain(t, m, "10.0.0."+string(rune('1'+i)), 30_000)
			mu.Lock()
			total += n
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	st, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if total > 95_000 || st.Period.Sent != total || st.Pending != 0 {
		t.Fatalf("delivered=%d confirmed=%d pending=%d, line=95000", total, st.Period.Sent, st.Pending)
	}
	if total < 95_000-1000 {
		t.Fatalf("budget was not usable up to the line: %d", total)
	}
	if st.State != StateExhausted {
		t.Fatalf("state=%s", st.State)
	}
	if _, err := m.Begin("10.9.9.9", CategoryPublic, 10); !errors.Is(err, ErrQuota) {
		t.Fatalf("new download after line: %v", err)
	}
}

func TestRestartKeepsUnconfirmedBudgetUntilReconcile(t *testing.T) {
	cfg := smallConfig(100_000)
	s, m, clk := setup(t, cfg)
	// 一个响应预留了预算但进程在确认前崩溃。
	st, err := m.Begin("1.1.1.1", CategoryPublic, 50_000)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.Grant(300)
	if err != nil || n != 300 {
		t.Fatalf("grant %d %v", n, err)
	}
	// 重启：新 Meter 复用同一数据库，旧响应不再存在。
	m2, err := New(s, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	m2.BlockSize = 1000
	status, err := m2.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Orphaned != 1000 || status.Used != 1000 {
		t.Fatalf("orphaned=%d used=%d: unconfirmed reservation must stay counted", status.Orphaned, status.Used)
	}
	// 重启和并发重复请求都拿不到完整额度：最多 95_000-1000。
	var got int64
	for i := 0; i < 3; i++ {
		got += drain(t, m2, "2.2.2.2", 95_000)
	}
	if got > 94_000 {
		t.Fatalf("restart regenerated budget: %d", got)
	}
	released, err := m2.Reconcile()
	if err != nil || released != 1000 {
		t.Fatalf("reconcile %d %v", released, err)
	}
	after, _ := m2.Status()
	if after.Orphaned != 0 || after.Period.Sent != got {
		t.Fatalf("confirmed bytes must be untouched: %+v", after)
	}
}

func TestPeriodsShortMonthsAndClockRollback(t *testing.T) {
	cases := []struct {
		now, want time.Time
		day       int
	}{
		{time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 31},
		{time.Date(2028, 3, 15, 0, 0, 0, 0, time.UTC), time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC), 31},
		{time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC), 31},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), 31},
	}
	for _, c := range cases {
		if got := PeriodStart(c.now, c.day); !got.Equal(c.want) {
			t.Errorf("PeriodStart(%s,%d)=%s want %s", c.now, c.day, got, c.want)
		}
	}
	if got := NextBoundary(time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 31); !got.Equal(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("NextBoundary=%s", got)
	}

	cfg := smallConfig(10_000)
	cfg.ResetDay = 31
	_, m, clk := setup(t, cfg)
	clk.set(time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	if drain(t, m, "3.3.3.3", 9_500) != 9_500 {
		t.Fatal("first period should allow the full line")
	}
	if _, err := m.Begin("3.3.3.3", CategoryPublic, 1); !errors.Is(err, ErrQuota) {
		t.Fatalf("exhausted: %v", err)
	}
	// 回拨时钟（以及改重置日）不能创建更早的周期来重复发放额度。
	clk.set(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	cfg.ResetDay = 1
	if err := m.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Begin("3.3.3.3", CategoryPublic, 1); !errors.Is(err, ErrQuota) {
		t.Fatalf("clock rollback must not mint a fresh allowance: %v", err)
	}
	// 时间前进到下一周期后恢复，历史周期被保留。
	clk.set(time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC))
	if drain(t, m, "3.3.3.3", 100) != 100 {
		t.Fatal("next period should allow downloads again")
	}
	status, _ := m.Status()
	if len(status.History) != 2 {
		t.Fatalf("history=%d", len(status.History))
	}
}

func TestIPLimitsAndUnknownQuota(t *testing.T) {
	cfg := smallConfig(100_000)
	cfg.IPConcurrency = 2
	cfg.IPPercent = 10 // 单 IP 9_500
	_, m, _ := setup(t, cfg)
	a, _ := m.Begin("9.9.9.9", CategoryPublic, 1000)
	b, _ := m.Begin("9.9.9.9", CategoryPublic, 1000)
	if _, err := m.Begin("9.9.9.9", CategoryPublic, 1000); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrency: %v", err)
	}
	_ = a.Close(false)
	_ = b.Close(false)
	if got := drain(t, m, "9.9.9.9", 50_000); got != 9_500 {
		t.Fatalf("per-IP byte limit delivered %d", got)
	}
	if drain(t, m, "8.8.8.8", 1000) != 1000 {
		t.Fatal("another source must remain unaffected")
	}
	cfg.QuotaBytes = 0
	if err := m.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st, _ := m.Status()
	if st.State != StateQuotaUnknown {
		t.Fatalf("unknown quota must be reported, got %s", st.State)
	}
}

func TestFullSharePercentEqualsTotalLine(t *testing.T) {
	cfg := smallConfig(10_007) // 线 = 10_007 - 500 = 9_507，不是 100 的整数倍
	_, m, _ := setup(t, cfg)
	if got := drain(t, m, "7.7.7.7", 50_000); got != 9_507 {
		t.Fatalf("a single source at 100%% must reach exactly the total line, got %d", got)
	}
	if _, err := m.Begin("7.7.7.7", CategoryPublic, 1); !errors.Is(err, ErrQuota) {
		t.Fatalf("at the line the reported reason must be the total quota, got %v", err)
	}
}
