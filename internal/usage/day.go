package usage

import (
	"database/sql"
	"errors"
)

func (m *Meter) day(tx *sql.Tx, sent, received, downloads int64) error {
	now := m.now().UTC()
	start := now.Unix() - int64(now.Hour()*3600+now.Minute()*60+now.Second())
	_, e := tx.Exec(`INSERT INTO usage_days(start,sent,received,downloads) VALUES(?,?,?,?) ON CONFLICT(start) DO UPDATE SET sent=sent+excluded.sent,received=received+excluded.received,downloads=downloads+excluded.downloads`, start, sent, received, downloads)
	return e
}
func (m *Meter) Today() (Period, error) {
	now := m.now().UTC()
	start := now.Unix() - int64(now.Hour()*3600+now.Minute()*60+now.Second())
	p := Period{Start: start, End: start + 86400}
	e := m.db.DB.QueryRow("SELECT sent,received,downloads FROM usage_days WHERE start=?", start).Scan(&p.Sent, &p.Received, &p.Downloads)
	if errors.Is(e, sql.ErrNoRows) {
		e = nil
	}
	return p, e
}
