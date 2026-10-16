package usage

import (
	"database/sql"
	"errors"
	"sync"
)

// Stream 代表一个活动响应的预算。典型用法：写循环 Grant→写→Confirm，另有周期 Flush。
type Stream struct {
	m        *Meter
	ip       string
	category string
	expect   int64

	mu          sync.Mutex
	reservedAll int64 // 本响应累计已预留
	rowID       int64
	rowPeriod   int64
	rowReserved int64
	rowFlushed  int64
	rowSent     int64
	sent        int64
	closed      bool
}

// Grant 返回当前可发送的字节数（≤want）。预算耗尽时预留新块；到线返回 ErrQuota/ErrIPLimit。
func (s *Stream) Grant(want int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("计量流已关闭")
	}
	if want <= 0 {
		return 0, nil
	}
	if s.rowReserved-s.rowSent <= 0 {
		if err := s.extend(); err != nil {
			return 0, err
		}
	}
	avail := s.rowReserved - s.rowSent
	if int64(want) > avail {
		want = int(avail)
	}
	return want, nil
}

// Confirm 在字节写入响应后确认。确认值被限制在已授予预算内。
func (s *Stream) Confirm(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := int64(n)
	if v > s.rowReserved-s.rowSent {
		v = s.rowReserved - s.rowSent
	}
	if v > 0 {
		s.rowSent += v
		s.sent += v
	}
}

func (s *Stream) Sent() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

// Flush 把内存中已确认的字节写入周期统计，缩小崩溃时的保守占用。
func (s *Stream) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.rowID == 0 || s.rowSent == s.rowFlushed {
		return nil
	}
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	tx, err := s.m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.flushTx(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.rowFlushed = s.rowSent
	return nil
}

func (s *Stream) flushTx(tx *sql.Tx) error {
	delta := s.rowSent - s.rowFlushed
	if delta <= 0 {
		return nil
	}
	if _, err := tx.Exec("UPDATE usage_periods SET sent=sent+? WHERE start=?", delta, s.rowPeriod); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO usage_categories(period_start,category,direction,bytes) VALUES(?,?,'sent',?)
		ON CONFLICT(period_start,category,direction) DO UPDATE SET bytes=bytes+excluded.bytes`, s.rowPeriod, s.category, delta); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO usage_ips(period_start,ip,bytes) VALUES(?,?,?)
		ON CONFLICT(period_start,ip) DO UPDATE SET bytes=bytes+excluded.bytes`, s.rowPeriod, s.ip, delta); err != nil {
		return err
	}
	_, err := tx.Exec("UPDATE usage_reservations SET flushed=flushed+?,updated_at=? WHERE id=?", delta, s.m.now().Unix(), s.rowID)
	return err
}

// extend 在一个事务内：落盘已确认字节 → 检查额度 → 预留新块。调用者持有 s.mu。
func (s *Stream) extend() error {
	cfg, err := s.m.Config()
	if err != nil {
		return err
	}
	block := s.m.BlockSize
	if block <= 0 {
		block = DefaultBlockSize
	}
	if s.expect >= 0 && block > s.expect-s.reservedAll {
		block = s.expect - s.reservedAll
	}
	if block <= 0 {
		return ErrBudget
	}
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	tx, err := s.m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.rowID != 0 {
		if err := s.flushTx(tx); err != nil {
			return err
		}
	}
	p, err := s.m.period(tx, cfg)
	if err != nil {
		return err
	}
	grant := block
	room, limited, err := s.m.headroom(tx, cfg, p, s.ip)
	if err != nil {
		return err
	}
	if limited && room < grant {
		grant = room
	}
	now := s.m.now().Unix()
	rowID, rowReserved := s.rowID, s.rowReserved
	sameRow := s.rowID != 0 && s.rowPeriod == p.Start
	if sameRow {
		if _, err := tx.Exec("UPDATE usage_reservations SET reserved=reserved+?,updated_at=? WHERE id=?", grant, now, s.rowID); err != nil {
			return err
		}
		rowReserved += grant
	} else {
		if s.rowID != 0 {
			if _, err := tx.Exec("DELETE FROM usage_reservations WHERE id=?", s.rowID); err != nil {
				return err
			}
		}
		res, err := tx.Exec("INSERT INTO usage_reservations(period_start,ip,category,reserved,state,created_at,updated_at) VALUES(?,?,?,?,'open',?,?)", p.Start, s.ip, s.category, grant, now, now)
		if err != nil {
			return err
		}
		rowID, _ = res.LastInsertId()
		rowReserved = grant
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if sameRow {
		s.rowFlushed = s.rowSent
	} else {
		s.rowSent, s.rowFlushed = 0, 0
	}
	s.rowID, s.rowPeriod, s.rowReserved = rowID, p.Start, rowReserved
	s.reservedAll += grant
	return nil
}

// Close 释放并发名额与未用预留；completed 表示完整交付一个文件，仅此时增加逻辑下载数。
func (s *Stream) Close(completed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if s.m.active[s.ip] <= 1 {
		delete(s.m.active, s.ip)
	} else {
		s.m.active[s.ip]--
	}
	if s.rowID == 0 && !completed {
		return nil
	}
	cfg, err := s.m.Config()
	if err != nil {
		return err
	}
	tx, err := s.m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.rowID != 0 {
		if err := s.flushTx(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM usage_reservations WHERE id=?", s.rowID); err != nil {
			return err
		}
	}
	if completed {
		p, err := s.m.period(tx, cfg)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE usage_periods SET downloads=downloads+1 WHERE start=?", p.Start); err != nil {
			return err
		}
	}
	return tx.Commit()
}
