//go:build linux

package install

import (
	"bytes"
	"crypto/ed25519"
	"debug/elf"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"filepanel/internal/backup"
	"filepanel/internal/release"
	"filepanel/internal/store"
)

type updateState struct {
	Phase    string `json:"phase"`
	Version  string `json:"version,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Tests inject bounded failures at durable phase boundaries, without executing
// a service manager or changing an unrelated instance.
var maintenanceService = service
var maintenanceReady = ready
var maintenanceReadyVersion = readyVersion
var updateFault func(string) error

func fault(stage string) error {
	if updateFault != nil {
		return updateFault(stage)
	}
	return nil
}

func startRecovered(m *manifest) error {
	if m.Init == "systemd" {
		if e := command("/usr/bin/systemctl", "reset-failed", serviceName(m.Config.Instance)+".service"); e != nil {
			return e
		}
	}
	if m.Init == "openrc" {
		if e := service(m, "zap"); e != nil {
			return e
		}
	}
	return maintenanceService(m, "start")
}

type updateJournal struct {
	Old    manifest
	Target release.Manifest
	Kind   string
	Phase  string
}

func staticELF(path string) error {
	f, e := elf.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	machine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = elf.EM_AARCH64
	}
	if f.Machine != machine {
		return errors.New("签名产物实际 ELF 架构不符")
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return errors.New("产物含外部动态加载器依赖")
		}
	}
	return nil
}

func configPath(m *manifest, s string) string { return base(m.Config.Instance) + "/config/" + s }
func journalPath(id string) string            { return registry + "/" + id + "-update.json" }
func atomicJSON(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return atomicBytes(path, b, 0600)
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func atomicBytes(path string, b []byte, mode fs.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".fp-write-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}
func signed(m *manifest, path string) (release.Manifest, release.Artifact, error) {
	key, e := base64.StdEncoding.DecodeString(m.Config.UpdateKey)
	if e != nil || len(key) != 32 {
		return release.Manifest{}, release.Artifact{}, errors.New("安装时未分发可信更新公钥")
	}
	f, e := os.Open(path)
	if e != nil {
		return release.Manifest{}, release.Artifact{}, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil {
		return release.Manifest{}, release.Artifact{}, e
	}
	r, a, e := release.Verify(b, ed25519.PublicKey(key), m.Sequence, runtime.GOARCH)
	if e == nil && m.ReleaseVersion != "" && !release.Newer(r.Version, m.ReleaseVersion) {
		e = errors.New("语义版本必须递增")
	}
	return r, a, e
}
func state(m *manifest, s updateState) error {
	return atomicJSON(configPath(m, "update-state.json"), s)
}
func stageUpdate(m *manifest, path string, out io.Writer) error {
	if _, e := os.Stat(journalPath(m.Config.Instance)); !os.IsNotExist(e) {
		return errors.New("未完成维护须先恢复")
	}
	r, a, e := signed(m, path)
	if e != nil {
		return e
	}
	src := filepath.Join(filepath.Dir(path), a.File)
	if e = release.VerifyArtifact(src, a); e != nil {
		return e
	}
	if e = staticELF(src); e != nil {
		return e
	}
	if e = backup.CheckSpace(base(m.Config.Instance), uint64(a.Size)+4<<20, m.Config.Reserve); e != nil {
		return e
	}
	dir, e := os.MkdirTemp(configPath(m, ""), ".staged-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	if e = copyExclusive(src, dir+"/candidate", 0755); e != nil {
		return e
	}
	if e = release.VerifyArtifact(dir+"/candidate", a); e != nil {
		return e
	}
	if e = copyExclusive(path, dir+"/release.json", 0600); e != nil {
		return e
	}
	// Reverify the copy, closing the source-manifest change window.
	if _, _, e = signed(m, dir+"/release.json"); e != nil {
		return e
	}
	if e = syncDir(dir); e != nil {
		return e
	}
	dest := configPath(m, "staged")
	if _, e = os.Lstat(dest); !os.IsNotExist(e) {
		return errors.New("已有预置更新；请先应用或由管理员移除 config/staged")
	}
	if e = os.Rename(dir, dest); e != nil {
		return e
	}
	if e = syncDir(filepath.Dir(dest)); e != nil {
		return e
	}
	if e = state(m, updateState{Phase: "staged", Version: r.Version, Sequence: r.Sequence}); e != nil {
		return e
	}
	fmt.Fprintln(out, "已预置可信更新", r.Version, "；网页可显式触发本机更新。")
	return nil
}
func stopLocked(m *manifest) (*os.File, error) {
	if e := maintenanceService(m, "stop"); e != nil {
		return nil, e
	}
	return store.LockDataDir(m.Config.Data)
}
func gated(m *manifest) error {
	return atomicBytes(configPath(m, "maintenance"), []byte("maintenance\n"), 0600)
}
func unlockGate(m *manifest) error {
	if e := os.Remove(configPath(m, "maintenance")); e != nil && !os.IsNotExist(e) {
		return e
	}
	return syncDir(configPath(m, ""))
}
func ownedTree(m *manifest, path string) error {
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("恢复目录出现链接")
		}
		return os.Chown(p, int(m.UID), int(m.GID))
	})
}
func dataAnchor(m *manifest) error {
	a, e := snapshot(m.Config.Data, m.UID)
	if e != nil {
		return e
	}
	m.Anchors[2] = a
	return nil
}

func removeRetiredData(m *manifest) error {
	for _, suffix := range []string{".previous", ".previous.rollback"} {
		p := m.Config.Data + suffix
		i, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return errors.New("退役数据目录身份异常，保留现场")
		}
		if e = trusted(filepath.Dir(p), 0); e != nil {
			return e
		}
		// This exact child was created by restoreData while the journal was
		// durable. The original is already in update-snapshot; never delete it.
		if e = os.RemoveAll(p); e != nil {
			return e
		}
	}
	return syncDir(filepath.Dir(m.Config.Data))
}
func instanceBackup(m *manifest, path string, out io.Writer) (err error) {
	if _, e := os.Lstat(journalPath(m.Config.Instance)); !os.IsNotExist(e) {
		return errors.New("已有维护日志，先 recover-update")
	}
	if path == "" {
		return errors.New("需要 --backup-path，备份在数据目录外")
	}
	if e := trusted(filepath.Dir(path), 0); e != nil {
		return e
	}
	n, e := backup.Estimate(m.Config.Data)
	if e != nil {
		return e
	}
	if e = backup.CheckSpace(filepath.Dir(path), uint64(n), m.Config.Reserve); e != nil {
		return e
	}
	if e = atomicJSON(journalPath(m.Config.Instance), updateJournal{Old: *m, Kind: "backup", Phase: "prepared"}); e != nil {
		return e
	}
	if e = gated(m); e != nil {
		return e
	}
	lock, e := stopLocked(m)
	if e != nil {
		return errors.Join(e, recoverUpdate(m.Config.Instance, out))
	}
	defer func() {
		lock.Close()
		re := maintenanceService(m, "start")
		if re == nil {
			re = maintenanceReady(m.Config)
		}
		if re == nil {
			re = unlockGate(m)
		}
		if re == nil {
			re = os.Remove(journalPath(m.Config.Instance))
		}
		err = errors.Join(err, re)
	}()
	v, e := backup.Create(m.Config.Data, path, m.Config.Reserve)
	if e != nil {
		return e
	}
	fmt.Fprintf(out, "一致性备份完成；本机 %d 项，远端 %d 个文件未包含。密钥在 SQLite 中，请加密离线保管。\n", len(v.Entries), v.RemoteFiles)
	return nil
}
func applyUpdate(m *manifest, out io.Writer) (err error) {
	if e := ensureNoMaintenance(m); e != nil {
		return e
	}
	if _, e := os.Stat(journalPath(m.Config.Instance)); !os.IsNotExist(e) {
		return errors.New("未完成维护须先 recover-update")
	}
	r, a, e := signed(m, configPath(m, "staged/release.json"))
	if e != nil {
		return e
	}
	if e = release.VerifyArtifact(configPath(m, "staged/candidate"), a); e != nil {
		return e
	}
	n, e := backup.Estimate(m.Config.Data)
	if e != nil {
		return e
	}
	// One snapshot + staged candidate + binary rollback + migration/restore peak.
	for _, p := range []string{base(m.Config.Instance), filepath.Dir(m.Config.Data)} {
		if e = backup.CheckSpace(p, uint64(2*n)+uint64(a.Size)+8<<20, m.Config.Reserve); e != nil {
			return e
		}
	}
	if _, e = os.Lstat(configPath(m, "update-snapshot")); !os.IsNotExist(e) {
		return errors.New("前次维护快照仍保留，请先移到离线备份后清理")
	}
	if _, e = os.Lstat(configPath(m, "previous-binary")); !os.IsNotExist(e) {
		return errors.New("前次回退程序仍保留，请先归档")
	}
	j := updateJournal{Old: *m, Target: r, Kind: "update", Phase: "prepared"}
	if e = atomicJSON(journalPath(m.Config.Instance), j); e != nil {
		return e
	}
	defer func() {
		if err != nil {
			re := recoverUpdate(m.Config.Instance, out)
			if re != nil {
				err = fmt.Errorf("%w；恢复仍需处理: %v", err, re)
			}
		}
	}()
	if e = gated(m); e != nil {
		return e
	}
	lock, e := stopLocked(m)
	if e != nil {
		return e
	}
	_, e = backup.Create(m.Config.Data, configPath(m, "update-snapshot"), m.Config.Reserve)
	lock.Close()
	if e != nil {
		return e
	}
	if e = copyExclusive(base(m.Config.Instance)+"/filepanel", configPath(m, "previous-binary"), 0755); e != nil {
		return e
	}
	j.Phase = "snapshotted"
	if e = atomicJSON(journalPath(m.Config.Instance), j); e != nil {
		return e
	}
	if e = state(m, updateState{Phase: "replacing", Version: r.Version, Sequence: r.Sequence}); e != nil {
		return e
	}
	if e = fault("replace"); e != nil {
		return e
	}
	if e = replaceBinary(m, configPath(m, "staged/candidate"), a.SHA256); e != nil {
		return e
	}
	if e = fault("migrate"); e != nil {
		return e
	}
	if e = maintenanceService(m, "start"); e != nil {
		return e
	}
	if e = maintenanceReadyVersion(m, r.Version); e != nil {
		return e
	}
	if e = fault("health"); e != nil {
		return e
	}
	// The candidate can migrate/read while external requests and background
	// workers remain behind the durable maintenance gate.
	if e = checkSnapshotData(m, configPath(m, "update-snapshot")); e != nil {
		return e
	}
	m.Sequence = r.Sequence
	m.ReleaseVersion = r.Version
	m.Hash = a.SHA256
	m.Files[base(m.Config.Instance)+"/filepanel"] = a.SHA256
	if e = save(m); e != nil {
		return e
	}
	j.Phase = "committed"
	if e = atomicJSON(journalPath(m.Config.Instance), j); e != nil {
		return e
	}
	// From committed onward recovery only finishes activation, never restores
	// an old snapshot and silently discards writes accepted by the new version.
	if e = unlockGate(m); e != nil {
		return e
	}
	if e = state(m, updateState{Phase: "complete", Version: r.Version, Sequence: r.Sequence}); e != nil {
		return e
	}
	if e = os.Remove(journalPath(m.Config.Instance)); e != nil {
		return e
	}
	_ = os.RemoveAll(configPath(m, "staged"))
	fmt.Fprintln(out, "更新与数据读取验证完成：", r.Version, "；原程序和一致性快照保留供离线归档。")
	return nil
}
func replaceBinary(m *manifest, src, hash string) error {
	dst := base(m.Config.Instance) + "/filepanel.next"
	if _, e := os.Lstat(dst); !os.IsNotExist(e) {
		// A crash during copy may leave this uncommitted fixed staging name.
		// Never touch the active binary or a link/user-controlled file here.
		if e = regularRoot(dst); e != nil {
			return e
		}
		if e = os.Remove(dst); e != nil {
			return e
		}
	}
	if e := copyExclusive(src, dst, 0755); e != nil {
		return e
	}
	defer os.Remove(dst)
	s, _, e := digestFile(dst)
	if e != nil || s != hash {
		return errors.New("替换程序哈希不匹配")
	}
	if m.Config.LowPorts && m.Init == "openrc" {
		if e = lowPortCapability(dst); e != nil {
			return e
		}
	}
	if e = os.Rename(dst, base(m.Config.Instance)+"/filepanel"); e != nil {
		return e
	}
	return syncDir(base(m.Config.Instance))
}
func readyVersion(m *manifest, version string) error {
	host, port, _ := net.SplitHostPort(m.Config.Listen)
	if net.ParseIP(host).IsUnspecified() {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer c.CloseIdleConnections()
	for i := 0; i < 40; i++ {
		r, e := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
		if e == nil {
			var got struct {
				Version string `json:"version"`
			}
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			r.Body.Close()
			if r.StatusCode == 200 && json.Unmarshal(b, &got) == nil && got.Version == version {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("新程序真实健康/版本检查失败")
}
func checkSnapshotData(m *manifest, path string) error {
	v, e := backup.Verify(path)
	if e != nil {
		return e
	}
	// Compare immutable payload files, then open a fresh SQLite connection to
	// validate that migrations preserved keys, sharing and file metadata.
	for _, f := range v.Entries {
		if f.Path == "filepanel.db" {
			continue
		}
		s, n, e := digestFile(filepath.Join(m.Config.Data, filepath.FromSlash(f.Path)))
		if e != nil || s != f.SHA256 || n != f.Size {
			return errors.New("更新后本机文件/素材读取校验失败")
		}
	}
	return compareDB(filepath.Join(path, "filepanel.db"), filepath.Join(m.Config.Data, "filepanel.db"))
}
func recoverUpdate(id string, out io.Writer) error {
	path := journalPath(id)
	if e := regularRoot(path); os.IsNotExist(e) {
		return nil
	} else if e != nil {
		return e
	}
	var j updateJournal
	if e := decodeFile(path, &j); e != nil {
		return e
	}
	m := j.Old
	if m.Config.Instance != id || Validate(m.Config) != nil || validData(m.Config.Data, id) != nil || len(m.Anchors) != 6 {
		return errors.New("维护日志路径/实例无效")
	}
	if j.Phase == "committed" || j.Phase == "rolled_back" {
		// load verifies the committed binary/config/service hashes.
		current, e := load(id)
		if e != nil {
			return e
		}
		if e = maintenanceService(current, "start"); e != nil {
			return e
		}
		if e = removeRetiredData(current); e != nil {
			return e
		}
		if e = unlockGate(current); e != nil {
			return e
		}
		phase, version, sequence := "complete", j.Target.Version, j.Target.Sequence
		if j.Phase == "rolled_back" {
			phase, version, sequence = "rolled_back", current.ReleaseVersion, current.Sequence
		}
		if e = state(current, updateState{Phase: phase, Version: version, Sequence: sequence}); e != nil {
			return e
		}
		return os.Remove(path)
	}
	if e := gated(&m); e != nil {
		return e
	}
	if e := maintenanceService(&m, "stop"); e != nil {
		return e
	}
	if j.Phase != "prepared" {
		if e := replaceBinary(&m, configPath(&m, "previous-binary"), m.Hash); e != nil {
			return e
		}
		if e := restoreDataSuffix(&m, configPath(&m, "update-snapshot"), ".rollback"); e != nil {
			return e
		}
	}
	if e := dataAnchor(&m); e != nil {
		return e
	}
	if e := save(&m); e != nil {
		return e
	}
	if e := startRecovered(&m); e != nil {
		return e
	}
	if e := maintenanceReady(m.Config); e != nil {
		return e
	}
	j.Phase = "rolled_back"
	if e := atomicJSON(path, j); e != nil {
		return e
	}
	if j.Kind != "backup" {
		if e := removeRetiredData(&m); e != nil {
			return e
		}
	}
	if e := unlockGate(&m); e != nil {
		return e
	}
	if e := state(&m, updateState{Phase: "rolled_back", Error: "未提交的维护已恢复原程序与一致性数据"}); e != nil {
		return e
	}
	if e := os.Remove(path); e != nil {
		return e
	}
	fmt.Fprintln(out, "已恢复原实例，保留失败现场与维护快照。")
	return nil
}
func restoreData(m *manifest, src string) error {
	return restoreDataSuffix(m, src, "")
}
func restoreDataSuffix(m *manifest, src, suffix string) error {
	newPath, oldPath := m.Config.Data+".replacement"+suffix, m.Config.Data+".previous"+suffix
	_, oldErr := os.Lstat(oldPath)
	_, curErr := os.Lstat(m.Config.Data)
	_, newErr := os.Lstat(newPath)
	if os.IsNotExist(oldErr) {
		if os.IsNotExist(newErr) {
			if _, e := backup.RestoreNew(src, newPath, m.Config.Reserve); e != nil {
				return e
			}
		}
		if e := ownedTree(m, newPath); e != nil {
			return e
		}
		if e := os.Rename(m.Config.Data, oldPath); e != nil {
			return e
		}
		if e := syncDir(filepath.Dir(m.Config.Data)); e != nil {
			return e
		}
		curErr = os.ErrNotExist
	}
	if os.IsNotExist(curErr) {
		if e := os.Rename(newPath, m.Config.Data); e != nil {
			return e
		}
	}
	return syncDir(filepath.Dir(m.Config.Data))
}
func instanceRestore(m *manifest, path string, out io.Writer) error {
	v, e := backup.Verify(path)
	if e != nil {
		return e
	}
	if v.Role != m.Config.Role {
		return errors.New("恢复角色不匹配")
	}
	// Explicit restore is allowed only with the same panel/node identity. New
	// installations import into a separate empty directory using backup restore.
	if e = compareIdentity(path, m.Config.Data); e != nil {
		return e
	}
	if e = trusted(path, 0); e != nil {
		return e
	}
	if e = ensureNoMaintenance(m); e != nil {
		return e
	}
	n, e := backup.Estimate(m.Config.Data)
	if e != nil {
		return e
	}
	var target int64
	for _, f := range v.Entries {
		target += f.Size
	}
	for _, p := range []string{base(m.Config.Instance), filepath.Dir(m.Config.Data)} {
		if e = backup.CheckSpace(p, uint64(2*n+target)+32<<20, m.Config.Reserve); e != nil {
			return e
		}
	}
	j := updateJournal{Old: *m, Target: release.Manifest{Version: release.Version, Sequence: m.Sequence}, Kind: "restore", Phase: "prepared"}
	if e = atomicJSON(journalPath(m.Config.Instance), j); e != nil {
		return e
	}
	return restoreTransaction(m, path, &j, out)
}

func ensureNoMaintenance(m *manifest) error {
	for _, p := range []string{journalPath(m.Config.Instance), configPath(m, "update-snapshot"), configPath(m, "previous-binary"), m.Config.Data + ".replacement", m.Config.Data + ".previous", m.Config.Data + ".replacement.rollback", m.Config.Data + ".previous.rollback"} {
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			return errors.New("上次维护现场尚未归档，请先核查并离线归档")
		}
	}
	return nil
}
func restoreTransaction(m *manifest, path string, j *updateJournal, out io.Writer) (err error) {
	defer func() {
		if err != nil {
			if re := recoverUpdate(m.Config.Instance, out); re != nil {
				err = fmt.Errorf("%w；恢复仍需处理: %v", err, re)
			}
		}
	}()
	if e := gated(m); e != nil {
		return e
	}
	lock, e := stopLocked(m)
	if e != nil {
		return e
	}
	_, e = backup.Create(m.Config.Data, configPath(m, "update-snapshot"), m.Config.Reserve)
	lock.Close()
	if e != nil {
		return e
	}
	if e = copyExclusive(base(m.Config.Instance)+"/filepanel", configPath(m, "previous-binary"), 0755); e != nil {
		return e
	}
	j.Phase = "snapshotted"
	if e = atomicJSON(journalPath(m.Config.Instance), j); e != nil {
		return e
	}
	if e = restoreData(m, path); e != nil {
		return e
	}
	if e = dataAnchor(m); e != nil {
		return e
	}
	if e = maintenanceService(m, "start"); e != nil {
		return e
	}
	if e = maintenanceReady(m.Config); e != nil {
		return e
	}
	if e = checkSnapshotData(m, path); e != nil {
		return e
	}
	if e = save(m); e != nil {
		return e
	}
	j.Phase = "committed"
	if e = atomicJSON(journalPath(m.Config.Instance), j); e != nil {
		return e
	}
	if e = removeRetiredData(m); e != nil {
		return e
	}
	if e = unlockGate(m); e != nil {
		return e
	}
	if e = state(m, updateState{Phase: "restored", Version: release.Version, Sequence: m.Sequence}); e != nil {
		return e
	}
	if e = os.Remove(journalPath(m.Config.Instance)); e != nil {
		return e
	}
	fmt.Fprintln(out, "恢复完成，原数据目录与回退快照保留；原身份、私有状态与分享配置已核对。")
	return nil
}
func updateAgent(args []string) error {
	f := flag.NewFlagSet("update-agent", flag.ContinueOnError)
	id := f.String("instance", "main", "固定本机实例")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 || validInstance(*id) != nil {
		return errors.New("维护代理参数无效")
	}
	if e := Command([]string{"maintain", "--instance", *id, "--action", "recover-update"}, bytes.NewReader(nil), io.Discard); e != nil {
		return e
	}
	m, e := load(*id)
	if e != nil {
		return e
	}
	dir := "/run/filepanel-" + *id
	if e = os.MkdirAll(dir, 0750); e != nil {
		return e
	}
	if e = os.Chown(dir, 0, int(m.GID)); e != nil {
		return e
	}
	if e = os.Chmod(dir, 0750); e != nil {
		return e
	}
	if e = trusted(dir, 0); e != nil {
		return e
	}
	path := dir + "/update.sock"
	if st, e := os.Lstat(path); e == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("代理套接字路径被占用")
		}
		if c, e := net.Dial("unix", path); e == nil {
			c.Close()
			return errors.New("已有维护代理")
		}
		if e = os.Remove(path); e != nil {
			return e
		}
	}
	l, e := net.Listen("unix", path)
	if e != nil {
		return e
	}
	defer l.Close()
	defer os.Remove(path)
	if e = os.Chown(path, 0, int(m.GID)); e != nil {
		return e
	}
	if e = os.Chmod(path, 0660); e != nil {
		return e
	}
	var mu sync.Mutex
	busy := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		var s updateState
		if e := decodeFile(configPath(m, "update-state.json"), &s); e != nil {
			s.Phase = "idle"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("POST /apply", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 {
			http.Error(w, "不接受参数", 400)
			return
		}
		mu.Lock()
		if busy {
			mu.Unlock()
			http.Error(w, "维护正在执行", 409)
			return
		}
		busy = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		io.WriteString(w, `{"phase":"accepted"}`)
		go func() {
			defer func() { mu.Lock(); busy = false; mu.Unlock() }()
			var out bytes.Buffer
			e := Command([]string{"maintain", "--instance", *id, "--action", "apply-update"}, bytes.NewReader(nil), &out)
			if e != nil {
				_ = state(m, updateState{Phase: "failed", Error: e.Error()})
			}
		}()
	})
	s := http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
	return s.Serve(l)
}
