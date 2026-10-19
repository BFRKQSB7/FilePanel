//go:build linux

package install

import (
	"bufio"
	"context"
	"database/sql"
	"debug/elf"
	"encoding/json"
	"errors"
	relmeta "filepanel/internal/release"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const registry = "/etc/filepanel"

type anchor struct {
	Path string
	Dev  uint64
	Ino  uint64
	UID  uint32
}
type manifest struct {
	Version        int
	Config         Config
	Init           string
	User           string
	UID            uint32
	GID            uint32
	Hash           string
	State          string
	Anchors        []anchor
	Files          map[string]string
	Sequence       uint64
	ReleaseVersion string
}

func base(id string) string         { return "/opt/filepanel-" + id }
func manifestPath(id string) string { return registry + "/" + id + ".json" }
func serviceName(id string) string  { return "filepanel-" + id }
func servicePath(m *manifest) string {
	if m.Init == "openrc" {
		return "/etc/init.d/" + serviceName(m.Config.Instance)
	}
	return "/etc/systemd/system/" + serviceName(m.Config.Instance) + ".service"
}

// No shell is used. Neither service users nor the web server have sudo privileges.
func command(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	b, e := c.CombinedOutput()
	if e != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, e, strings.TrimSpace(string(b)))
	}
	return nil
}
func service(m *manifest, action string) error {
	if m.Init == "openrc" {
		return command("/sbin/rc-service", serviceName(m.Config.Instance), action)
	}
	return command("/usr/bin/systemctl", action, serviceName(m.Config.Instance)+".service")
}
func enable(m *manifest, on bool) error {
	if m.Init == "openrc" {
		action := "add"
		if !on {
			action = "del"
			if _, e := os.Lstat("/etc/runlevels/default/" + serviceName(m.Config.Instance)); os.IsNotExist(e) {
				return nil
			}
		}
		return command("/sbin/rc-update", action, serviceName(m.Config.Instance), "default")
	}
	action := "enable"
	if !on {
		action = "disable"
	}
	return command("/usr/bin/systemctl", action, serviceName(m.Config.Instance)+".service")
}

func platform(body string, systemd, openrc bool) (string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			values[k] = strings.Trim(v, "\"'")
		}
	}
	switch values["ID"] {
	case "alpine":
		if openrc {
			return "openrc", nil
		}
	case "debian":
		if systemd {
			return "systemd", nil
		}
	case "ubuntu":
		if values["VERSION_ID"] == "24.04" && systemd {
			return "systemd", nil
		}
	}
	return "", errors.New("需要真实 Alpine/OpenRC、Debian/systemd 或 Ubuntu 24.04 LTS/systemd；不支持仅有 init 命令的空容器")
}
func detect() (string, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", errors.New("仅支持 amd64/arm64")
	}
	b, e := os.ReadFile("/etc/os-release")
	if e != nil {
		return "", e
	}
	_, se := os.Stat("/run/systemd/system")
	_, oe := os.Stat("/run/openrc/softlevel")
	return platform(string(b), se == nil, oe == nil)
}

// Every ancestor must be canonical and root owned; mutable ancestors cannot
// redirect privileged writes between validation and use.
func trusted(path string, finalUID uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\n\r\t %$'\"\\?#;") {
		return errors.New("路径必须是无转义字符的规范绝对路径")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	p := "/"
	for i, v := range parts {
		p = filepath.Join(p, v)
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		s, ok := st.Sys().(*syscall.Stat_t)
		uid := uint32(0)
		if i == len(parts)-1 {
			uid = finalUID
		}
		if !ok || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || s.Uid != uid || st.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("目录所有权、权限或链接不安全: %s", p)
		}
	}
	return nil
}
func snapshot(path string, uid uint32) (anchor, error) {
	if e := trusted(path, uid); e != nil {
		return anchor{}, e
	}
	st, e := os.Lstat(path)
	if e != nil {
		return anchor{}, e
	}
	s := st.Sys().(*syscall.Stat_t)
	return anchor{path, uint64(s.Dev), s.Ino, uid}, nil
}
func (m *manifest) validate() error {
	if m.Version != 1 || validInstance(m.Config.Instance) != nil || m.User != "fp-"+m.Config.Instance || m.UID == 0 || m.GID == 0 || len(m.Anchors) != 6 || Validate(m.Config) != nil {
		return errors.New("未知或损坏的安装清单")
	}
	account, e := user.Lookup(m.User)
	if e != nil || account.Uid != strconv.FormatUint(uint64(m.UID), 10) || account.Gid != strconv.FormatUint(uint64(m.GID), 10) {
		return errors.New("服务账号身份已变化，拒绝维护")
	}
	expected := []string{base(m.Config.Instance), base(m.Config.Instance) + "/config", m.Config.Data, base(m.Config.Instance) + "/cache", base(m.Config.Instance) + "/logs", base(m.Config.Instance) + "/backups"}
	if e := validData(m.Config.Data, m.Config.Instance); e != nil {
		return e
	}
	for i, a := range m.Anchors {
		uid := m.UID
		if i < 2 {
			uid = 0
		}
		if a.Path != expected[i] || a.UID != uid {
			return errors.New("清单路径或所有权越界")
		}
		now, e := snapshot(a.Path, a.UID)
		if e != nil {
			return e
		}
		if a != now {
			return fmt.Errorf("目录身份已变更，拒绝清理: %s", a.Path)
		}
	}
	if m.Init != "systemd" && m.Init != "openrc" {
		return errors.New("未知 init")
	}
	allowed := map[string]bool{base(m.Config.Instance) + "/runtime.json": true, base(m.Config.Instance) + "/filepanel": true, servicePath(m): true}
	wantFiles := 3
	if m.Config.UpdateKey != "" {
		allowed[agentPath(m)] = true
		wantFiles++
	}
	if len(m.Files) != wantFiles {
		return errors.New("安装文件清单损坏")
	}
	for path, want := range m.Files {
		if !allowed[path] {
			return errors.New("清单文件越界")
		}
		if m.State == "uninstalled" && path != base(m.Config.Instance)+"/runtime.json" {
			continue
		}
		if e := regularRoot(path); e != nil {
			return e
		}
		got, _, e := digestFile(path)
		if e != nil || got != want {
			return fmt.Errorf("安装文件被修改: %s", path)
		}
	}
	return nil
}
func regularRoot(path string) error {
	if e := trusted(filepath.Dir(path), 0); e != nil {
		return e
	}
	i, e := os.Lstat(path)
	if e != nil {
		return e
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || !i.Mode().IsRegular() || s.Uid != 0 || i.Mode().Perm()&0022 != 0 || s.Nlink != 1 {
		return fmt.Errorf("须为 root 独占普通文件: %s", path)
	}
	return nil
}
func validData(path, id string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "filepanel-"+id || strings.Count(path, "/") < 3 || strings.HasPrefix(path, base(id)+"/") {
		return errors.New("数据目录必须是专用的 filepanel-实例名 子目录，不能指向整盘或共享目录")
	}
	return nil
}
func load(id string) (*manifest, error) {
	path := manifestPath(id)
	if e := regularRoot(path); e != nil {
		return nil, e
	}
	var m manifest
	if e := decodeFile(path, &m); e != nil {
		return nil, e
	}
	if m.Config.Instance != id {
		return nil, errors.New("清单实例不匹配")
	}
	return &m, m.validate()
}
func save(m *manifest) error {
	return atomicJSON(manifestPath(m.Config.Instance), m)
}

func Command(args []string, in io.Reader, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("生命周期维护只允许管理员；未安装 setuid、sudoers 或任意命令 helper")
	}
	if len(args) == 0 {
		return errors.New("需要 install/uninstall/maintain")
	}
	if args[0] == "update-agent" {
		return updateAgent(args[1:])
	}
	os.Setenv("PATH", "/usr/sbin:/usr/bin:/sbin:/bin")
	syscall.Umask(0077)
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	id := flags.String("instance", "main", "本机隔离实例名")
	rel := flags.String("release", "", "本地可信 release.json；在线发布尚未配置")
	pin := flags.String("manifest-sha256", "", "独立可信来源的清单 SHA256")
	role := flags.String("role", "panel", "panel 或 node")
	listen := flags.String("listen", "127.0.0.1:8080", "明确 IP:端口；node 使用回环 HTTPS 反代")
	data := flags.String("data-dir", "", "新建专用目录；须以 filepanel-实例名 结尾")
	join := flags.String("join-file", "", "0600 入网 JSON（不进入命令日志）")
	low := flags.Bool("low-ports", false, "授予绑定低端口的必要能力")
	reserve := flags.Uint64("reserve-bytes", 16<<20, "至少 16MiB，另保留磁盘 5%")
	updateKey := flags.String("update-key", "", "离线分发的可信 Ed25519 公钥（base64）")
	backupPath := flags.String("backup-path", "", "实例外的私有备份目录")
	purge := flags.Bool("purge", false, "删除本实例拥有的数据与凭据")
	action := flags.String("action", "status", "start/stop/restart/status/init-token")
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("不接受位置参数")
	}
	if e := validInstance(*id); e != nil {
		return e
	}
	if e := os.MkdirAll(registry, 0700); e != nil {
		return e
	}
	if e := trusted(registry, 0); e != nil {
		return e
	}
	lock, e := os.OpenFile(registry+"/.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return errors.New("另一个安装/维护操作正在执行")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	switch args[0] {
	case "install":
		if *data == "" {
			*data = "/var/lib/filepanel-" + *id
		}
		return installInstance(Config{Instance: *id, Role: *role, Data: *data, Listen: *listen, LowPorts: *low, Reserve: *reserve, JoinFile: *join, UpdateKey: *updateKey}, *rel, *pin, out)
	case "uninstall":
		m, e := load(*id)
		if e != nil {
			return e
		}
		return uninstall(m, *purge, in, out)
	case "maintain":
		if *action == "recover-update" {
			return recoverUpdate(*id, out)
		}
		m, e := load(*id)
		if e != nil {
			return e
		}
		if m.State != "active" {
			return errors.New("实例未处于 active；请用原配置重新安装")
		}
		switch *action {
		case "stage-update":
			return stageUpdate(m, *rel, out)
		case "apply-update":
			return applyUpdate(m, out)
		case "backup":
			return instanceBackup(m, *backupPath, out)
		case "restore":
			return instanceRestore(m, *backupPath, out)
		case "start", "stop", "restart", "status":
			if e = service(m, *action); e != nil {
				return e
			}
			if *action == "start" || *action == "restart" {
				if e = ready(m.Config); e != nil {
					return e
				}
			}
			fmt.Fprintln(out, "服务动作完成:", *action)
			return nil
		case "init-token":
			return asUser(m, []string{"init-token", "--data-dir", m.Config.Data}, out)
		default:
			return errors.New("维护动作不在固定允许清单中")
		}
	}
	return errors.New("未知生命周期命令")
}

func copyExclusive(src, dst string, mode fs.FileMode) error {
	f, e := os.Open(src)
	if e != nil {
		return e
	}
	defer f.Close()
	g, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	if e = g.Chmod(mode); e != nil {
		g.Close()
		os.Remove(dst)
		return e
	}
	_, e = io.Copy(g, f)
	if e == nil {
		e = g.Sync()
	}
	ce := g.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(dst)
	}
	return e
}
func put(path string, b []byte, mode fs.FileMode) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	if e = f.Chmod(mode); e != nil {
		f.Close()
		os.Remove(path)
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(path)
	}
	return e
}

func installInstance(c Config, release, pin string, out io.Writer) (err error) {
	joinInput := c.JoinFile
	c.JoinFile = ""
	if e := Validate(c); e != nil {
		return e
	}
	if e := validData(c.Data, c.Instance); e != nil {
		return e
	}
	init, e := detect()
	if e != nil {
		return e
	}
	src, a, e := VerifyRelease(release, pin)
	if e != nil {
		return e
	}
	var initialVersion string
	initialSequence, _ := strconv.ParseUint(relmeta.Sequence, 10, 64)
	if c.UpdateKey != "" {
		r, _, e := signed(&manifest{Config: c}, release)
		if e != nil {
			return e
		}
		initialSequence, initialVersion = r.Sequence, r.Version
	}
	f, e := elf.Open(src)
	if e != nil {
		return e
	}
	machine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = elf.EM_AARCH64
	}
	valid := f.Machine == machine
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			valid = false
		}
	}
	f.Close()
	if !valid {
		return errors.New("产物架构不符或存在外部动态加载器依赖")
	}
	m, e := load(c.Instance)
	_, manifestError := os.Lstat(manifestPath(c.Instance))
	fresh := os.IsNotExist(manifestError)
	if e != nil && !fresh {
		return e
	}
	if fresh || m.State == "uninstalled" {
		listener, e := net.Listen("tcp", c.Listen)
		if e != nil {
			return fmt.Errorf("安装前端口检查失败: %w", e)
		}
		listener.Close()
	}
	if fresh {
		root := base(c.Instance)
		if e = trusted(filepath.Dir(root), 0); e != nil {
			return e
		}
		if e = trusted(filepath.Dir(c.Data), 0); e != nil {
			return e
		}
		for _, path := range []string{filepath.Dir(root), filepath.Dir(c.Data)} {
			var stat unix.Statfs_t
			if e = unix.Statfs(path, &stat); e != nil {
				return e
			}
			total := stat.Blocks * uint64(stat.Bsize)
			free := stat.Bavail * uint64(stat.Bsize)
			safety := c.Reserve
			if total/20 > safety {
				safety = total / 20
			}
			need := uint64(a.Size) + 4<<20
			if free < safety || free-safety < need {
				return errors.New("安装空间预检失败；保留非零安全余量与二进制/数据库临时峰值")
			}
		}
		if _, e = os.Lstat(root); !os.IsNotExist(e) {
			return errors.New("程序目录已存在且无可信清单，拒绝接管")
		}
		if _, e = os.Lstat(c.Data); !os.IsNotExist(e) {
			return errors.New("数据目录已存在且无可信清单，拒绝接管")
		}
		name := "fp-" + c.Instance
		if _, e = user.Lookup(name); e == nil {
			return errors.New("同名用户已存在，拒绝接管")
		}
		probe := &manifest{Config: c, Init: init}
		if _, e = os.Lstat(servicePath(probe)); !os.IsNotExist(e) {
			return errors.New("服务名冲突，拒绝覆盖")
		}
		if c.UpdateKey != "" {
			if _, e = os.Lstat(agentPath(probe)); !os.IsNotExist(e) {
				return errors.New("维护服务名冲突")
			}
		}
		if init == "openrc" {
			if e = command("/usr/sbin/addgroup", "-S", name); e != nil {
				return e
			}
			e = command("/usr/sbin/adduser", "-S", "-D", "-H", "-G", name, "-s", "/sbin/nologin", name)
			if e != nil {
				_ = command("/usr/sbin/delgroup", name)
			}
		} else {
			e = command("/usr/sbin/useradd", "--system", "--no-create-home", "--user-group", "--shell", "/usr/sbin/nologin", name)
		}
		if e != nil {
			return e
		}
		u, e := user.Lookup(name)
		if e != nil {
			return e
		}
		uid, _ := strconv.ParseUint(u.Uid, 10, 32)
		gid, _ := strconv.ParseUint(u.Gid, 10, 32)
		m = &manifest{Version: 1, Config: c, Init: init, User: name, UID: uint32(uid), GID: uint32(gid), Hash: a.SHA256, State: "installing", Files: map[string]string{}}
		m.Sequence, m.ReleaseVersion = initialSequence, initialVersion
		// Before the durable manifest exists, no daemon runs. Roll back only paths
		// successfully created by this invocation, in reverse order, non-recursively.
		var created []string
		recorded := false
		defer func() {
			if err != nil && !recorded {
				for i := len(created) - 1; i >= 0; i-- {
					_ = os.Remove(created[i])
				}
				if init == "openrc" {
					_ = command("/usr/sbin/deluser", name)
					_ = command("/usr/sbin/delgroup", name)
				} else {
					_ = command("/usr/sbin/userdel", name)
				}
			}
		}()
		dirs := []string{root, root + "/config", c.Data, root + "/cache", root + "/logs", root + "/backups"}
		for i, p := range dirs {
			mode := fs.FileMode(0700)
			owner := m.UID
			if i < 2 {
				owner = 0
				mode = 0750
			}
			if e = os.Mkdir(p, mode); e != nil {
				return e
			}
			created = append(created, p)
			if e = os.Chmod(p, mode); e != nil {
				return e
			}
			if e = os.Chown(p, int(owner), int(m.GID)); e != nil {
				return e
			}
			sn, e := snapshot(p, owner)
			if e != nil {
				return e
			}
			m.Anchors = append(m.Anchors, sn)
		}
		b, _ := json.Marshal(c)
		if e = put(root+"/runtime.json", b, 0640); e != nil {
			return e
		}
		created = append(created, root+"/runtime.json")
		if e = os.Chown(root+"/runtime.json", 0, int(m.GID)); e != nil {
			return e
		}
		if e = copyExclusive(src, root+"/filepanel", 0755); e != nil {
			return e
		}
		created = append(created, root+"/filepanel")
		copied, n, e := digestFile(root + "/filepanel")
		if e != nil || copied != a.SHA256 || n != a.Size {
			return errors.New("复制后产物校验失败")
		}
		if e = put(servicePath(m), []byte(unit(m)), 0644); e != nil {
			return e
		}
		created = append(created, servicePath(m))
		if init == "openrc" {
			if e = os.Chmod(servicePath(m), 0755); e != nil {
				return e
			}
		}
		paths := []string{root + "/runtime.json", root + "/filepanel", servicePath(m)}
		if c.UpdateKey != "" {
			if e = putAgent(m); e != nil {
				return e
			}
			created = append(created, agentPath(m))
			paths = append(paths, agentPath(m))
		}
		for _, path := range paths {
			sum, _, e := digestFile(path)
			if e != nil {
				return e
			}
			m.Files[path] = sum
		}
		if e = save(m); e != nil {
			return e
		}
		recorded = true
	} else {
		// Repeat install is recovery, never an implicit update or config reset.
		if c != m.Config || a.SHA256 != m.Hash || init != m.Init {
			return errors.New("实例配置/产物冲突；使用原配置重装，更新由独立维护协议处理")
		}
		if m.State == "uninstalled" {
			if e = copyExclusive(src, base(c.Instance)+"/filepanel", 0755); e != nil {
				return e
			}
			if e = put(servicePath(m), []byte(unit(m)), 0644); e != nil {
				return e
			}
			if e = putAgent(m); e != nil {
				return e
			}
			if init == "openrc" {
				if e = os.Chmod(servicePath(m), 0755); e != nil {
					return e
				}
			}
			m.State = "installing"
			if e = save(m); e != nil {
				return e
			}
		}
	}
	defer func() {
		if err != nil {
			_ = service(m, "stop")
			_ = enable(m, false)
			fmt.Fprintln(out, "安装未完成，保留本地清单和数据；修正原因后用相同配置重试")
		}
	}()
	if c.LowPorts && init == "openrc" {
		if e = lowPortCapability(base(c.Instance) + "/filepanel"); e != nil {
			return e
		}
	}
	if c.Role == "node" {
		dbPath := filepath.Join(c.Data, "filepanel.db")
		enrolled := false
		if db, e := sql.Open("sqlite", "file:"+dbPath+"?mode=ro"); e == nil {
			var count int
			qe := db.QueryRow("SELECT COUNT(*) FROM settings WHERE key='node_identity'").Scan(&count)
			enrolled = qe == nil && count == 1
			db.Close()
		}
		if !enrolled {
			if joinInput == "" {
				return errors.New("新节点需要 --join-file；未入网不会报告安装成功")
			}
			i, e := os.Lstat(joinInput)
			if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 || i.Size() > 128<<10 {
				return errors.New("入网文件必须是 0600 普通文件")
			}
			target := base(c.Instance) + "/config/join.json"
			if e = copyExclusive(joinInput, target, 0640); e != nil {
				return e
			}
			defer os.Remove(target)
			if e = os.Chown(target, 0, int(m.GID)); e != nil {
				return e
			}
			if e = asUser(m, []string{"node", "join", "--data-dir", c.Data, "--join-file", target}, io.Discard); e != nil {
				return errors.New("节点入网失败；请核查票据、面板身份及 TLS 后重试")
			}
		}
	}
	if init == "systemd" {
		if e = command("/usr/bin/systemctl", "daemon-reload"); e != nil {
			return e
		}
	}
	if e = enable(m, true); e != nil {
		return e
	}
	if e = service(m, "start"); e != nil {
		return e
	}
	if e = ready(c); e != nil {
		return e
	}
	if e = service(m, "status"); e != nil {
		return e
	}
	m.State = "active"
	if e = save(m); e != nil {
		return e
	}
	if e = agentEnable(m, true); e != nil {
		return e
	}
	if e = agentService(m, "start"); e != nil {
		return e
	}
	fmt.Fprintf(out, "本机实例 %s 已安装并启动（%s，专用用户 %s）。\n", c.Instance, init, m.User)
	if c.Role == "panel" {
		fmt.Fprintf(out, "候选入口 http://%s/admin；公网/NAT 可达性须另行核对。\n", c.Listen)
		fmt.Fprintf(out, "初始化：sudo %s/filepanel maintain --instance %s --action init-token（令牌仅输出到此终端）\n", base(c.Instance), c.Instance)
	} else {
		fmt.Fprintln(out, "节点已入网并监听；公开 HTTPS 反代的端到端可达性仍需在面板检查。")
	}
	return nil
}

func asUser(m *manifest, args []string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, base(m.Config.Instance)+"/filepanel", args...)
	c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: m.UID, Gid: m.GID, Groups: []uint32{}}}
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + m.Config.Data, "TMPDIR=" + base(m.Config.Instance) + "/cache"}
	c.Stdout = out
	c.Stderr = io.Discard
	return c.Run()
}
func ready(c Config) error {
	host, port, _ := net.SplitHostPort(c.Listen)
	if net.ParseIP(host).IsUnspecified() {
		host = "127.0.0.1"
	}
	address := net.JoinHostPort(host, port)
	client := http.Client{Timeout: time.Second}
	for i := 0; i < 40; i++ {
		{
			if r, e := client.Get("http://" + address + "/healthz"); e == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					return nil
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("服务健康检查失败；未报告成功，检查本实例 logs/service.log")
}

func unit(m *manifest) string {
	root := base(m.Config.Instance)
	execLine := root + "/filepanel service-run --config " + root + "/runtime.json"
	if m.Init == "openrc" {
		return fmt.Sprintf("#!/sbin/openrc-run\nname=%s\nsupervisor=supervise-daemon\ncommand=%s/filepanel\ncommand_args=\"service-run --config %s/runtime.json\"\ncommand_user=%s:%s\npidfile=/run/%s.pid\nrespawn_delay=3\nrespawn_max=5\nrespawn_period=60\nexport TMPDIR=%s/cache\nexport GOMEMLIMIT=48MiB\nexport GOGC=50\ndepend() { need net; }\n", serviceName(m.Config.Instance), root, root, m.User, m.User, serviceName(m.Config.Instance), root)
	}
	caps := ""
	if m.Config.LowPorts {
		caps = "CAP_NET_BIND_SERVICE"
	}
	return fmt.Sprintf("[Unit]\nDescription=FilePanel local instance %s\nAfter=network.target\nStartLimitIntervalSec=60\nStartLimitBurst=5\n[Service]\nType=simple\nUser=%s\nGroup=%s\nExecStart=%s\nRestart=on-failure\nRestartSec=3\nTimeoutStopSec=20\nKillMode=control-group\nUMask=0077\nNoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=strict\nProtectHome=true\nReadWritePaths=%s %s/cache %s/logs %s/backups\nCapabilityBoundingSet=%s\nAmbientCapabilities=%s\nEnvironment=TMPDIR=%s/cache GOMEMLIMIT=48MiB GOGC=50\nStandardOutput=null\nStandardError=null\n[Install]\nWantedBy=multi-user.target\n", m.Config.Instance, m.User, m.User, execLine, m.Config.Data, root, root, root, caps, caps, root)
}

// The database is queried read-only without migrations/recovery, and never used
// as a source of commands. Missing/corrupt data stays explicitly unknown.
func remoteSummary(data string, out io.Writer) {
	fmt.Fprintln(out, "远端清单（数据库记录，非实时扫描；超过 35 秒标过期）：")
	if i, e := os.Lstat(filepath.Join(data, "filepanel.db")); e != nil || !i.Mode().IsRegular() {
		fmt.Fprintln(out, "未知：数据库缺失或不是普通文件")
		return
	}
	db, e := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(data, "filepanel.db"))+"?mode=ro&_pragma=busy_timeout(1000)")
	if e != nil {
		fmt.Fprintln(out, "未知：数据库无法读取")
		return
	}
	defer db.Close()
	rows, e := db.Query(`SELECT n.endpoint,n.public_ip,n.note,n.last_seen,COUNT(l.file_id),COALESCE(SUM(f.size),0) FROM nodes n LEFT JOIN node_locations l ON l.node_id=n.id LEFT JOIN files f ON f.id=l.file_id GROUP BY n.id`)
	if e != nil {
		fmt.Fprintln(out, "未知：数据库无法读取或无有效清单")
		return
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var endpoint, ip, note string
		var seen, n, size int64
		if rows.Scan(&endpoint, &ip, &note, &seen, &n, &size) != nil {
			fmt.Fprintln(out, "未知：记录无法读取")
			return
		}
		label := "最近记录"
		if seen == 0 || time.Now().Unix()-seen > 35 {
			label = "过期"
		}
		fmt.Fprintf(out, "地址=%q IP=%q 备注=%q 最后在线=%s [%s] 关联文件=%d 记录字节=%d\n", endpoint, ip, note, time.Unix(seen, 0).UTC().Format(time.RFC3339), label, n, size)
		count++
	}
	if rows.Err() != nil {
		fmt.Fprintln(out, "未知：读取中断")
	} else if count == 0 {
		fmt.Fprintln(out, "数据库中无远端节点记录（不代表远端磁盘为空）")
	}
}
func purgeCheck(m *manifest) error {
	if e := m.validate(); e != nil {
		return e
	}
	// Walk without following links; refuse nested mounts and replacements.
	for _, a := range m.Anchors {
		if a.Path == base(m.Config.Instance)+"/config" {
			continue
		}
		e := filepath.WalkDir(a.Path, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("拒绝链接清理: %s", path)
			}
			i, e := d.Info()
			if e != nil {
				return e
			}
			if uint64(i.Sys().(*syscall.Stat_t).Dev) != a.Dev {
				return fmt.Errorf("拒绝跨挂载点: %s", path)
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return nil
}
func uninstall(m *manifest, purge bool, in io.Reader, out io.Writer) error {
	if m.Config.Role == "panel" {
		remoteSummary(m.Config.Data, out)
	}
	mode := "保留数据、身份、凭据及安装清单"
	if purge {
		mode = "删除本实例数据、配置、日志、缓存、备份和凭据，重装不会自动恢复管理关系"
	}
	fmt.Fprintf(out, "只卸载本机 %s：%s。远端程序和文件保留；面板卸载后分享入口不可用，节点按失联租约停止授权。\n输入 yes 确认一次，其他输入取消：", m.Config.Instance, mode)
	line, e := bufio.NewReader(in).ReadString('\n')
	if e != nil && e != io.EOF {
		return e
	}
	if strings.TrimSpace(line) != "yes" {
		fmt.Fprintln(out, "已取消，未修改实例")
		return nil
	}
	if purge {
		if e = purgeCheck(m); e != nil {
			return e
		}
	}
	if m.State != "uninstalled" {
		if e = agentService(m, "stop"); e != nil {
			return e
		}
		if e = agentEnable(m, false); e != nil {
			return e
		}
		if e = service(m, "stop"); e != nil {
			return e
		}
		if e = enable(m, false); e != nil {
			return e
		}
		// Revalidate after the daemon has stopped, before any recursive deletion.
		if purge {
			if e = purgeCheck(m); e != nil {
				return e
			}
		}
		if e = os.Remove(servicePath(m)); e != nil {
			return e
		}
		if m.Config.UpdateKey != "" {
			if e = os.Remove(agentPath(m)); e != nil {
				return e
			}
		}
		if e = os.Remove(base(m.Config.Instance) + "/filepanel"); e != nil {
			return e
		}
		if m.Init == "systemd" {
			if e = command("/usr/bin/systemctl", "daemon-reload"); e != nil {
				return e
			}
		}
		m.State = "uninstalled"
		if e = save(m); e != nil {
			return e
		}
	}
	if purge {
		// Dedicated child only: never RemoveAll a mount root or shared parent.
		if e = purgeCheck(m); e != nil {
			return e
		}
		if m.Init == "openrc" {
			e = command("/usr/sbin/deluser", m.User)
			if _, ge := user.LookupGroup(m.User); e == nil && ge == nil {
				e = command("/usr/sbin/delgroup", m.User)
			}
		} else {
			e = command("/usr/sbin/userdel", m.User)
		}
		if e != nil {
			return e
		}
		for _, p := range []string{m.Config.Data, base(m.Config.Instance)} {
			if e = os.RemoveAll(p); e != nil {
				return e
			}
		}
		if e = os.Remove(manifestPath(m.Config.Instance)); e != nil {
			return e
		}
	}
	fmt.Fprintln(out, "本机卸载完成；远端程序/文件仍保留，未发送任何远端删除命令。")
	return nil
}
