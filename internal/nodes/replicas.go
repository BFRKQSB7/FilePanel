package nodes

import (
	"context"
	"errors"
)

type Placement struct {
	Node    string `json:"node"`
	Object  string `json:"-"`
	SHA     string `json:"sha256"`
	Size    int64  `json:"size"`
	State   string `json:"state"`
	Primary bool   `json:"primary"`
}

func (p *Panel) Placements(file string) ([]Placement, error) {
	v, err := p.Files.Metadata(file)
	if err != nil {
		return nil, err
	}
	obj, err := p.activeObject(file)
	if err != nil {
		return nil, err
	}
	result := []Placement{{Node: v.StorageNode, Object: obj, SHA: v.SHA256, Size: v.Size, State: "active", Primary: true}}
	rows, err := p.DB.DB.Query("SELECT node_id,object_id,sha256,size,state FROM file_replicas WHERE file_id=? ORDER BY node_id", file)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var loc Placement
		if err = rows.Scan(&loc.Node, &loc.Object, &loc.SHA, &loc.Size, &loc.State); err != nil {
			return nil, err
		}
		result = append(result, loc)
	}
	return result, rows.Err()
}

func (p *Panel) RemoveReplica(requestID, file, node string) (Move, error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	if len(requestID) < 24 || len(requestID) > 100 {
		return Move{}, errors.New("请求 ID 长度无效")
	}
	if m, err := scanMove(p.DB.DB.QueryRow(moveSelect+" WHERE request_id=?", requestID)); err == nil {
		if m.FileID != file || m.Source != node || m.Kind != "remove" {
			return Move{}, ErrDenied
		}
		return m, nil
	}
	v, err := p.Files.Metadata(file)
	if err != nil {
		return Move{}, err
	}
	if node == v.StorageNode {
		return Move{}, errors.New("不能移除主位置；请先移动文件，至少保留一个完整位置")
	}
	m := Move{ID: random(), RequestID: requestID, FileID: file, Source: node, SHA: v.SHA256, Size: v.Size, Name: v.Name, State: "cleaning", Kind: "remove", Updated: p.Now().Unix()}
	tx, err := p.DB.DB.Begin()
	if err != nil {
		return Move{}, err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("SELECT object_id FROM file_replicas WHERE file_id=? AND node_id=? AND state='active'", file, node).Scan(&m.SourceObject); err != nil {
		return Move{}, err
	}
	// The existing per-file task index serializes moves, copies and removals.
	_, err = tx.Exec(`INSERT INTO file_moves(id,request_id,file_id,source,source_object,target,target_object,sha256,size,name,state,kind,updated) VALUES(?,?,?,?,?,'',?,?,?,?, 'cleaning','remove',?)`, m.ID, requestID, file, node, m.SourceObject, random(), m.SHA, m.Size, m.Name, m.Updated)
	if err != nil {
		return Move{}, errors.New("此文件已有未完成的存储任务")
	}
	if _, err = tx.Exec("UPDATE file_replicas SET state='removing' WHERE file_id=? AND node_id=?", file, node); err != nil {
		return Move{}, err
	}
	return m, tx.Commit()
}

func (p *Panel) finishReplicaRemoval(m Move) error {
	tx, err := p.DB.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fence int64
	if err = tx.QueryRow("SELECT fence FROM file_moves WHERE id=? AND state='cleaning' AND kind='remove'", m.ID).Scan(&fence); err != nil || fence != m.Fence {
		return ErrDenied
	}
	if _, err = tx.Exec("DELETE FROM file_replicas WHERE file_id=? AND node_id=? AND object_id=? AND state='removing'", m.FileID, m.Source, m.SourceObject); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE file_moves SET state='done',error='',updated=? WHERE id=?", p.Now().Unix(), m.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Panel) sourcePlacement(ctx context.Context, file, relay string) (Placement, error) {
	all, err := p.Placements(file)
	if err != nil {
		return Placement{}, err
	}
	// Prefer an immutable copy on the requested exit; otherwise retain the
	// primary order and fall back only to another verified committed copy.
	for i, loc := range all {
		if loc.Node == relay && loc.State == "active" {
			all[0], all[i] = all[i], all[0]
			break
		}
	}
	for _, loc := range all {
		if loc.State != "active" {
			continue
		}
		if loc.Node == "" {
			v, handle, e := p.Files.OpenObject(loc.Object, loc.SHA)
			if handle != nil {
				handle.Close()
			}
			if e == nil && v.SHA256 == loc.SHA && v.Size == loc.Size {
				return loc, nil
			}
			continue
		}
		n, e := p.Get(loc.Node)
		if e != nil || n.Revoked || p.Now().Unix() > n.LastSeen+30 {
			continue
		}
		if p.Probe(ctx, n.ID) == nil {
			return loc, nil
		}
	}
	return Placement{}, errors.New("没有可用的已校验存储位置")
}

func (p *Panel) currentPlacement(t Transfer) bool {
	all, err := p.Placements(t.File)
	if err != nil {
		return false
	}
	for _, loc := range all {
		if loc.Node == t.Source && loc.Object == p.transferObject(t) && loc.SHA == t.SHA && loc.State == "active" {
			return true
		}
	}
	return false
}

// A file is not removed while replicas still need durable cleanup. Repeated
// deletion advances one removal at a time; an offline replica stays recorded.
func (p *Panel) deleteReplicas(ctx context.Context, file string) error {
	var count int
	if err := p.DB.DB.QueryRow("SELECT COUNT(*) FROM file_replicas WHERE file_id=?", file).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	all, err := p.Placements(file)
	if err != nil {
		return err
	}
	for _, loc := range all {
		if loc.Primary {
			continue
		}
		if loc.State == "removing" {
			p.ReconcileMoves(ctx)
			return errors.New("副本清理尚未完成，请稍后重试删除")
		}
		_, err := p.RemoveReplica(random(), file, loc.Node)
		if err != nil {
			return err
		}
		p.ReconcileMoves(ctx)
	}
	var remaining int
	if err = p.DB.DB.QueryRow("SELECT COUNT(*) FROM file_replicas WHERE file_id=?", file).Scan(&remaining); err != nil {
		return err
	}
	if remaining > 0 {
		return errors.New("远端副本待重连清理，文件和副本记录均已保留")
	}
	return nil
}
