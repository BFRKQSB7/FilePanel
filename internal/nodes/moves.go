package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"filepanel/internal/files"
)

type Move struct {
	ID           string `json:"id"`
	RequestID    string `json:"request_id"`
	FileID       string `json:"file_id"`
	Source       string `json:"source"`
	SourceObject string `json:"-"`
	Target       string `json:"target"`
	TargetObject string `json:"-"`
	SHA          string `json:"-"`
	Size         int64  `json:"size"`
	Name         string `json:"name"`
	State        string `json:"state"`
	Fence        int64  `json:"fence"`
	Progress     int64  `json:"progress"`
	Route        string `json:"route"`
	Error        string `json:"error"`
	Cancel       bool   `json:"cancel"`
	Updated      int64  `json:"updated"`
	Kind         string `json:"kind"`
	ReuseObject  string `json:"-"`
}

const moveSelect = `SELECT id,request_id,file_id,source,source_object,target,target_object,sha256,size,name,state,fence,progress,route,error,cancel,updated,kind,reuse_object FROM file_moves`

func scanMove(r interface{ Scan(...any) error }) (Move, error) {
	var m Move
	e := r.Scan(&m.ID, &m.RequestID, &m.FileID, &m.Source, &m.SourceObject, &m.Target, &m.TargetObject, &m.SHA, &m.Size, &m.Name, &m.State, &m.Fence, &m.Progress, &m.Route, &m.Error, &m.Cancel, &m.Updated, &m.Kind, &m.ReuseObject)
	return m, e
}
func (p *Panel) Move(id string) (Move, error) {
	return scanMove(p.DB.DB.QueryRow(moveSelect+" WHERE id=?", id))
}
func (p *Panel) Moves() ([]Move, error) {
	rows, e := p.DB.DB.Query(moveSelect + " ORDER BY state IN ('done','cancelled'),updated DESC LIMIT 100")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	all := []Move{}
	for rows.Next() {
		m, e := scanMove(rows)
		if e != nil {
			return nil, e
		}
		all = append(all, m)
	}
	return all, rows.Err()
}
func (p *Panel) activeObject(id string) (string, error) {
	var key string
	e := p.DB.DB.QueryRow(`SELECT COALESCE(o.object_id,CASE WHEN f.object_key LIKE 'remote:%' THEN f.id ELSE f.object_key END) FROM files f LEFT JOIN file_objects o ON o.file_id=f.id WHERE f.id=?`, id).Scan(&key)
	return key, e
}
func (p *Panel) transferObject(t Transfer) string {
	var obj string
	if p.DB.DB.QueryRow("SELECT object_id FROM transfer_objects WHERE transfer_id=?", t.ID).Scan(&obj) != nil {
		return t.File
	}
	return obj
}
func (p *Panel) oldReader(t Transfer) bool {
	var count int
	_ = p.DB.DB.QueryRow("SELECT COUNT(*) FROM file_moves WHERE file_id=? AND source=? AND source_object=? AND state='cleaning'", t.File, t.Source, p.transferObject(t)).Scan(&count)
	return count > 0
}

func (p *Panel) StartMove(ctx context.Context, requestID, file, target string) (Move, error) {
	return p.startMove(ctx, requestID, file, target, "move")
}
func (p *Panel) StartCopy(ctx context.Context, requestID, file, target string) (Move, error) {
	return p.startMove(ctx, requestID, file, target, "copy")
}
func (p *Panel) startMove(ctx context.Context, requestID, file, target, kind string) (Move, error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	if len(requestID) < 24 || len(requestID) > 100 {
		return Move{}, errors.New("请求 ID 长度无效")
	}
	if m, e := scanMove(p.DB.DB.QueryRow(moveSelect+" WHERE request_id=?", requestID)); e == nil {
		if m.FileID != file || m.Target != target || (m.Kind != kind && !(kind == "move" && m.Kind == "move_existing")) {
			return Move{}, ErrDenied
		}
		return m, nil
	}
	v, e := p.Files.Metadata(file)
	if e != nil {
		return Move{}, e
	}
	if v.StorageNode == target {
		return Move{}, errors.New("文件已在此存储位置")
	}
	if target != "" {
		if _, e = p.usable(ctx, target, true); e != nil {
			return Move{}, e
		}
	}
	obj, e := p.activeObject(file)
	if e != nil {
		return Move{}, e
	}
	m := Move{ID: random(), RequestID: requestID, FileID: file, Source: v.StorageNode, SourceObject: obj, Target: target, TargetObject: random(), SHA: v.SHA256, Size: v.Size, Name: v.Name, State: "preflight", Updated: p.Now().Unix(), Kind: kind}
	var existing, replicaState string
	if err := p.DB.DB.QueryRow("SELECT object_id,state FROM file_replicas WHERE file_id=? AND node_id=?", file, target).Scan(&existing, &replicaState); err == nil {
		if kind == "copy" || replicaState != "active" {
			return Move{}, errors.New("此位置已有副本或正在移除，请先处理已有副本")
		}
		m.Kind = "move_existing"
		m.ReuseObject = existing
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Move{}, err
	}
	// INSERT SELECT and the deletion trigger serialize move/delete across processes.
	res, e := p.DB.DB.Exec(`INSERT INTO file_moves(id,request_id,file_id,source,source_object,target,target_object,sha256,size,name,updated,kind,reuse_object) SELECT ?,?,?,?,?,?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM files WHERE id=? AND state='available')`, m.ID, requestID, file, m.Source, obj, target, m.TargetObject, m.SHA, m.Size, m.Name, m.Updated, m.Kind, m.ReuseObject, file)
	if e != nil {
		return Move{}, errors.New("此文件已有未完成移动，请先处理原任务")
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return Move{}, ErrDenied
	}
	return m, nil
}
func (p *Panel) RetryMove(id string) error {
	_, e := p.DB.DB.Exec("UPDATE file_moves SET error='',updated=? WHERE id=? AND state NOT IN ('done','cancelled')", p.Now().Unix(), id)
	return e
}
func (p *Panel) CancelMove(id string) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	res, e := p.DB.DB.Exec("UPDATE file_moves SET cancel=1,fence=fence+1,error='',updated=? WHERE id=? AND state IN ('preflight','transferring','verifying','switching')", p.Now().Unix(), id)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("主位置已切换，只能继续源清理")
	}
	return nil
}
func (p *Panel) fault(stage string) error {
	if p.MoveFault != nil {
		return p.MoveFault(stage)
	}
	return nil
}
func (p *Panel) moveUpdate(m Move, state string) error {
	res, e := p.DB.DB.Exec("UPDATE file_moves SET state=?,error='',updated=? WHERE id=? AND fence=?", state, p.Now().Unix(), m.ID, m.Fence)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrDenied
	}
	return nil
}
func (p *Panel) operation(m Move, action, node, obj string) (string, error) {
	id := random()
	_, e := p.DB.DB.Exec("INSERT OR IGNORE INTO move_operations(id,move_id,fence,action,node_id,object_id) VALUES(?,?,?,?,?,?)", id, m.ID, m.Fence, action, node, obj)
	if e == nil {
		e = p.DB.DB.QueryRow("SELECT id FROM move_operations WHERE move_id=? AND fence=? AND action=? AND node_id=? AND object_id=?", m.ID, m.Fence, action, node, obj).Scan(&id)
	}
	return p.ticket(id, "source"), e
}

// Object verification may read a large file before returning headers. Control
// calls retain their short deadline; data calls remain cancellable by the worker
// generation and shutdown context without imposing that control deadline.
func (p *Panel) moveClient(n Node) (*http.Client, error) {
	control, e := p.client(n)
	if e != nil {
		return nil, e
	}
	transport := control.Transport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 0
	transport.TLSHandshakeTimeout = 8 * time.Second
	data := *control
	data.Transport = transport
	return &data, nil
}
func (p *Panel) movePermit(n Node, in Check) (Permit, bool, error) {
	var moveID, action, node, obj string
	var fence int64
	e := p.DB.DB.QueryRow("SELECT move_id,fence,action,node_id,object_id FROM move_operations WHERE id=?", in.Transfer).Scan(&moveID, &fence, &action, &node, &obj)
	if errors.Is(e, sql.ErrNoRows) {
		return Permit{}, false, nil
	}
	if e != nil {
		return Permit{}, true, e
	}
	m, e := p.Move(moveID)
	if e != nil || m.Fence != fence || node != n.ID || in.Role != "source" {
		return Permit{}, true, ErrDenied
	}
	allowed := false
	switch action {
	case "move_write":
		allowed = !m.Cancel && n.Storage && (m.State == "transferring" || m.State == "verifying" || m.State == "switching")
	case "move_read":
		allowed = !m.Cancel && (m.State == "transferring" || m.State == "verifying")
	case "move_clean":
		allowed = m.State == "cleaning" || m.State == "cancelling"
	}
	if !allowed {
		return Permit{}, true, ErrDenied
	}
	if action == "move_clean" {
		var retained int
		if err := p.DB.DB.QueryRow("SELECT COUNT(*) FROM file_replicas WHERE file_id=? AND node_id=? AND object_id=? AND state='active'", m.FileID, node, obj).Scan(&retained); err != nil || retained > 0 {
			return Permit{}, true, ErrDenied
		}
		active, e := p.activeObject(m.FileID)
		if e != nil {
			return Permit{}, true, e
		}
		v, e := p.Files.Metadata(m.FileID)
		if e != nil {
			return Permit{}, true, e
		}
		if active == obj && v.StorageNode == node {
			return Permit{}, true, ErrDenied
		}
	}
	out := Permit{Control: p.control(n, in.Nonce), Action: action, ObjectID: obj, File: files.Item{ID: obj, Name: m.Name, Size: m.Size, SHA256: m.SHA}}
	if action == "move_write" {
		if m.Source == "" {
			out.Source, _, e = p.DB.Setting("node_panel_endpoint")
			out.SourceCA, _, _ = p.DB.Setting("node_panel_ca")
		} else {
			src, se := p.Get(m.Source)
			if se != nil || src.Revoked {
				return Permit{}, true, ErrDenied
			}
			out.Source, out.SourceCA = src.Endpoint, src.CA
		}
		if e != nil {
			return Permit{}, true, e
		}
		out.Ticket, e = p.operation(m, "move_read", m.Source, m.SourceObject)
		if e != nil {
			return Permit{}, true, e
		}
	}
	return out, true, nil
}

// One bounded worker owns a task in this process; durable generations fence
// callbacks from previous runs and cancelled workers. Data-dir locking fences
// another panel process before it can acquire the same database.
func (p *Panel) ReconcileMoves(ctx context.Context) {
	if !p.moveWorker.TryLock() {
		return
	}
	defer p.moveWorker.Unlock()
	list, e := p.Moves()
	if e != nil {
		return
	}
	for _, m := range list {
		if ctx.Err() != nil {
			return
		}
		if m.State == "done" || m.State == "cancelled" || (m.Error != "" && m.State != "cleaning" && m.State != "cancelling") {
			continue
		}
		p.opsMu.Lock()
		res, e := p.DB.DB.Exec("UPDATE file_moves SET fence=fence+1 WHERE id=? AND fence=?", m.ID, m.Fence)
		if e == nil {
			n, _ := res.RowsAffected()
			if n != 1 {
				e = ErrDenied
			}
		}
		m.Fence++
		p.opsMu.Unlock()
		if e != nil {
			continue
		}
		e = p.runMove(ctx, m)
		if e != nil && ctx.Err() == nil {
			_, _ = p.DB.DB.Exec("UPDATE file_moves SET error=?,updated=? WHERE id=? AND fence=?", e.Error(), p.Now().Unix(), m.ID, m.Fence)
		}
	}
}

func (p *Panel) runMove(parent context.Context, m Move) error {
	if m.Kind == "move_existing" {
		m.TargetObject = m.ReuseObject
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				cur, e := p.Move(m.ID)
				if e != nil || cur.Fence != m.Fence {
					cancel()
					return
				}
			}
		}
	}()
	if m.Cancel || m.State == "cancelling" {
		if e := p.moveUpdate(m, "cancelling"); e != nil {
			return e
		}
		m.State = "cancelling"
		if e := func() error {
			if m.Kind == "move_existing" {
				return nil
			}
			return p.cleanupMove(ctx, m, false)
		}(); e != nil {
			return e
		}
		return p.moveUpdate(m, "cancelled")
	}
	if m.State == "cleaning" {
		if e := p.cleanupMove(ctx, m, true); e != nil {
			return e
		}
		if m.Kind == "remove" {
			return p.finishReplicaRemoval(m)
		}
		return p.moveUpdate(m, "done")
	}
	// Preflight reserves destination headroom before any byte is sent. Continuous
	// chunk checks still apply, including concurrent writes on the destination.
	if m.Target == "" {
		if e := p.Files.CheckSpace(uint64(m.Size)); e != nil {
			return e
		}
	} else if _, e := p.usable(ctx, m.Target, true); e != nil {
		return e
	}
	if e := p.moveUpdate(m, "transferring"); e != nil {
		return e
	}
	m.State = "transferring"
	if e := func() error {
		if m.Kind == "move_existing" {
			return nil
		}
		return p.copyMove(ctx, m)
	}(); e != nil {
		return e
	}
	if e := p.fault("after_transfer"); e != nil {
		return e
	}
	if e := p.moveUpdate(m, "verifying"); e != nil {
		return e
	}
	m.State = "verifying"
	if e := p.verifyMove(ctx, m); e != nil {
		return e
	}
	if e := p.fault("after_verify"); e != nil {
		return e
	}
	if e := p.moveUpdate(m, "switching"); e != nil {
		return e
	}
	m.State = "switching"
	if e := p.fault("before_switch"); e != nil {
		return e
	}
	if e := p.switchMove(m); e != nil {
		return e
	}
	if m.Kind == "copy" {
		return nil
	}
	m.State = "cleaning"
	if e := p.fault("after_switch"); e != nil {
		return e
	}
	if e := p.cleanupMove(ctx, m, true); e != nil {
		return e
	}
	return p.moveUpdate(m, "done")
}

func (p *Panel) moveSource(ctx context.Context, m Move) (io.ReadCloser, error) {
	if m.Source == "" {
		_, f, e := p.Files.OpenObject(m.SourceObject, m.SHA)
		return f, e
	}
	n, e := p.Get(m.Source)
	if e != nil || n.Revoked {
		return nil, ErrDenied
	}
	c, e := p.client(n)
	if e != nil {
		return nil, e
	}
	ticket, e := p.operation(m, "move_read", n.ID, m.SourceObject)
	if e != nil {
		return nil, e
	}
	r := &remoteReader{ctx: ctx, client: c, endpoint: n.Endpoint, ticket: ticket, size: m.Size, meter: p.Meter, ip: "move:" + m.ID, category: "move_ingress"}
	if _, e = r.Seek(0, io.SeekStart); e != nil {
		r.Close()
		return nil, e
	}
	return r, nil
}

type moveProgress struct {
	p       *Panel
	m       Move
	r       io.Reader
	n, last int64
}

type moveFaultReader struct {
	readSeekCloser
	panel *Panel
}
type readSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

func (r *moveFaultReader) Read(b []byte) (int, error) {
	n, e := r.readSeekCloser.Read(b)
	if n > 0 {
		if fault := r.panel.fault("during_transfer"); fault != nil {
			return n, fault
		}
	}
	return n, e
}

func (r *moveProgress) Read(b []byte) (int, error) {
	n, e := r.r.Read(b)
	r.n += int64(n)
	if r.n-r.last >= 256<<10 || e == io.EOF {
		r.last = r.n
		_, _ = r.p.DB.DB.Exec("UPDATE file_moves SET progress=? WHERE id=? AND fence=?", r.n, r.m.ID, r.m.Fence)
	}
	if n > 0 {
		if fault := r.p.fault("during_transfer"); fault != nil {
			return n, fault
		}
	}
	return n, e
}
func (p *Panel) copyMove(ctx context.Context, m Move) error {
	// Reuse a fully committed immutable target after a lost response or restart.
	if p.verifyMove(ctx, m) == nil {
		return nil
	}
	if m.Target != "" {
		n, e := p.Get(m.Target)
		if e != nil || n.Revoked {
			return ErrDenied
		}
		c, e := p.moveClient(n)
		if e != nil {
			return e
		}
		defer c.CloseIdleConnections()
		ticket, e := p.operation(m, "move_write", n.ID, m.TargetObject)
		if e != nil {
			return e
		}
		_, _ = p.DB.DB.Exec("UPDATE file_moves SET route='direct' WHERE id=? AND fence=?", m.ID, m.Fence)
		var v files.Item
		if call(ctx, c, "POST", n.Endpoint, prefix+"/pull", ticket, nil, &v) == nil && v.ID == m.TargetObject && v.SHA256 == m.SHA && v.Size == m.Size {
			_, _ = p.DB.DB.Exec("UPDATE file_moves SET progress=? WHERE id=? AND fence=?", m.Size, m.ID, m.Fence)
			return p.fault("during_transfer")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	src, e := p.moveSource(ctx, m)
	if e != nil {
		return e
	}
	defer src.Close()
	r := &moveProgress{p: p, m: m, r: src}
	if m.Target == "" {
		_, e = p.Files.UploadStaged(ctx, m.TargetObject, m.Name, m.Size, r, func() error { return p.fault("during_transfer") })
		_, _ = p.DB.DB.Exec("UPDATE file_moves SET route='direct' WHERE id=? AND fence=?", m.ID, m.Fence)
		return e
	}
	n, e := p.Get(m.Target)
	if e != nil || n.Revoked {
		return ErrDenied
	}
	c, e := p.moveClient(n)
	if e != nil {
		return e
	}
	defer c.CloseIdleConnections()
	ticket, e := p.operation(m, "move_write", n.ID, m.TargetObject)
	if e != nil {
		return e
	}
	stream, e := p.Meter.Begin("move:"+m.ID, "move_egress", m.Size)
	if e != nil {
		return e
	}
	defer stream.Close(false)
	req, e := http.NewRequestWithContext(ctx, "PUT", n.Endpoint+prefix+"/object", &sentReader{source: r, stream: stream})
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+ticket)
	_, _ = p.DB.DB.Exec("UPDATE file_moves SET route='panel' WHERE id=? AND fence=?", m.ID, m.Fence)
	res, e := c.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return errors.New("目标写入失败，源文件保留")
	}
	var v files.Item
	if e = json.NewDecoder(io.LimitReader(res.Body, 16<<10)).Decode(&v); e != nil {
		return e
	}
	if v.ID != m.TargetObject || v.SHA256 != m.SHA || v.Size != m.Size {
		return errors.New("目标校验失败，源文件保留")
	}
	return p.fault("during_transfer")
}
func (p *Panel) verifyMove(ctx context.Context, m Move) error {
	if m.Target == "" {
		return p.Files.VerifyObject(m.TargetObject, m.SHA, m.Size)
	}
	n, e := p.Get(m.Target)
	if e != nil || n.Revoked {
		return ErrDenied
	}
	c, e := p.moveClient(n)
	if e != nil {
		return e
	}
	defer c.CloseIdleConnections()
	ticket, e := p.operation(m, "move_write", n.ID, m.TargetObject)
	if e != nil {
		return e
	}
	var v files.Item
	e = call(ctx, c, "GET", n.Endpoint, prefix+"/object", ticket, nil, &v)
	if e != nil {
		return e
	}
	if v.ID != m.TargetObject || v.SHA256 != m.SHA || v.Size != m.Size {
		return errors.New("目标大小或 SHA-256 校验失败")
	}
	return nil
}
func (p *Panel) switchMove(m Move) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	tx, e := p.DB.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var fence int64
	var cancel bool
	var state, source, obj, sha string
	if e = tx.QueryRow("SELECT fence,cancel,state FROM file_moves WHERE id=?", m.ID).Scan(&fence, &cancel, &state); e != nil {
		return e
	}
	if fence != m.Fence || cancel || state != "switching" {
		return ErrDenied
	}
	e = tx.QueryRow(`SELECT COALESCE(l.node_id,''),COALESCE(o.object_id,CASE WHEN f.object_key LIKE 'remote:%' THEN f.id ELSE f.object_key END),f.sha256 FROM files f LEFT JOIN node_locations l ON l.file_id=f.id LEFT JOIN file_objects o ON o.file_id=f.id WHERE f.id=? AND f.state='available'`, m.FileID).Scan(&source, &obj, &sha)
	if e != nil || source != m.Source || obj != m.SourceObject || sha != m.SHA {
		return errors.New("主位置身份冲突，保留全部对象")
	}
	if m.Kind == "copy" {
		if _, e = tx.Exec("INSERT INTO file_replicas(file_id,node_id,object_id,sha256,size) VALUES(?,?,?,?,?)", m.FileID, m.Target, m.TargetObject, m.SHA, m.Size); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE file_moves SET state='done',progress=?,error='',updated=? WHERE id=? AND fence=?", m.Size, p.Now().Unix(), m.ID, m.Fence); e != nil {
			return e
		}
		return tx.Commit()
	}
	if _, e = tx.Exec("DELETE FROM file_replicas WHERE file_id=? AND node_id=? AND object_id=? AND state='active'", m.FileID, m.Target, m.TargetObject); e != nil {
		return e
	}
	key := m.TargetObject
	if m.Target != "" {
		key = "remote:" + m.TargetObject
	}
	if _, e = tx.Exec("UPDATE files SET object_key=?,updated_at=? WHERE id=?", key, p.Now().Unix(), m.FileID); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO file_objects(file_id,object_id) VALUES(?,?) ON CONFLICT(file_id) DO UPDATE SET object_id=excluded.object_id", m.FileID, m.TargetObject); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM node_locations WHERE file_id=?", m.FileID); e != nil {
		return e
	}
	if m.Target != "" {
		if _, e = tx.Exec("INSERT INTO node_locations(file_id,node_id,sha256) VALUES(?,?,?)", m.FileID, m.Target, m.SHA); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("UPDATE file_moves SET state='cleaning',progress=?,error='',updated=? WHERE id=? AND fence=?", m.Size, p.Now().Unix(), m.ID, m.Fence); e != nil {
		return e
	}
	return tx.Commit()
}
func (p *Panel) cleanupMove(ctx context.Context, m Move, source bool) error {
	if e := p.fault("cleanup"); e != nil {
		return e
	}
	node, obj := m.Target, m.TargetObject
	if source {
		node, obj = m.Source, m.SourceObject
	}
	p.opsMu.Lock()
	if source {
		var until int64
		e := p.DB.DB.QueryRow(`SELECT COALESCE(MAX(l.until),0) FROM node_transfer_leases l JOIN node_transfers t ON t.id=l.transfer_id LEFT JOIN transfer_objects o ON o.transfer_id=t.id WHERE t.action='download' AND t.file_id=? AND t.source=? AND COALESCE(o.object_id,t.file_id)=?`, m.FileID, node, obj).Scan(&until)
		if e != nil {
			p.opsMu.Unlock()
			return e
		}
		if until >= p.Now().Unix() {
			p.opsMu.Unlock()
			return errors.New("目标已切换，源读租约尚未结束；可重试清理")
		}
	}
	// No new old-source permission can pass once this generation authorizes cleanup.
	cur, e := p.Move(m.ID)
	if e != nil || cur.Fence != m.Fence {
		p.opsMu.Unlock()
		return ErrDenied
	}
	if node == "" {
		e = p.Files.CleanupObject(obj, m.SHA, false)
		p.opsMu.Unlock()
		return e
	}
	p.opsMu.Unlock()
	n, e := p.Get(node)
	if e != nil || n.Revoked {
		return errors.New("源清理待重连重试，目标已切换")
	}
	c, e := p.client(n)
	if e != nil {
		return e
	}
	ticket, e := p.operation(m, "move_clean", node, obj)
	if e != nil {
		return e
	}
	return call(ctx, c, "DELETE", n.Endpoint, prefix+"/object", ticket, nil, nil)
}
