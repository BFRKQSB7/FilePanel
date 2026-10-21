//go:build linux

package install

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

func readOnly(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)")
}
func queryBytes(db *sql.DB, q string) ([]byte, error) {
	r, e := db.Query(q)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	cols, e := r.Columns()
	if e != nil {
		return nil, e
	}
	var out bytes.Buffer
	for r.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if e = r.Scan(p...); e != nil {
			return nil, e
		}
		if e = json.NewEncoder(&out).Encode(v); e != nil {
			return nil, e
		}
	}
	return out.Bytes(), r.Err()
}
func compareDB(a, b string) error {
	x, e := readOnly(a)
	if e != nil {
		return e
	}
	defer x.Close()
	y, e := readOnly(b)
	if e != nil {
		return e
	}
	defer y.Close()
	queries := []string{"SELECT * FROM files ORDER BY id", "SELECT * FROM shares ORDER BY file_id", "SELECT * FROM assets ORDER BY id", "SELECT * FROM site_profile ORDER BY id", "SELECT * FROM settings ORDER BY key", "SELECT * FROM node_locations ORDER BY file_id", "SELECT id,credential,credential_hash,endpoint,revoked FROM nodes ORDER BY id"}
	var replicas int
	if err := x.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='file_replicas'").Scan(&replicas); err != nil {
		return err
	}
	if replicas > 0 {
		queries = append(queries, "SELECT * FROM file_replicas ORDER BY file_id,node_id")
	}
	for _, q := range queries {
		// Compare every original column; additive migrations may introduce new
		// defaults, but must never drop or change the snapshot's existing data.
		if strings.Contains(q, "*") {
			r, err := x.Query(q + " LIMIT 0")
			if err != nil {
				return err
			}
			cols, err := r.Columns()
			r.Close()
			if err != nil {
				return err
			}
			for i, c := range cols {
				cols[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
			}
			q = strings.Replace(q, "*", strings.Join(cols, ","), 1)
		}
		v, e := queryBytes(x, q)
		if e != nil {
			return e
		}
		w, e := queryBytes(y, q)
		if e != nil {
			return e
		}
		if !bytes.Equal(v, w) {
			return errors.New("迁移后元数据/公开状态/身份校验失败")
		}
	}
	return nil
}
func compareIdentity(a, b string) error {
	x, e := readOnly(filepath.Join(a, "filepanel.db"))
	if e != nil {
		return e
	}
	defer x.Close()
	y, e := readOnly(filepath.Join(b, "filepanel.db"))
	if e != nil {
		return e
	}
	defer y.Close()
	q := "SELECT key,value FROM settings WHERE key IN ('node_identity','node_panel_key') ORDER BY key"
	v, e := queryBytes(x, q)
	if e != nil {
		return e
	}
	w, e := queryBytes(y, q)
	if e != nil {
		return e
	}
	if !bytes.Equal(v, w) {
		return errors.New("原地恢复身份不匹配；请在隔离实例核对节点身份")
	}
	return nil
}
