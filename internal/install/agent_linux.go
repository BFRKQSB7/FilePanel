//go:build linux

package install

import (
	"encoding/binary"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// Linux's file capability xattr avoids depending on an optional setcap utility
// on minimal Alpine images. Only CAP_NET_BIND_SERVICE is granted.
func lowPortCapability(path string) error {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint32(b[0:4], 0x02000001)
	binary.LittleEndian.PutUint32(b[4:8], 1<<10)
	return unix.Setxattr(path, "security.capability", b, 0)
}

func agentPath(m *manifest) string {
	if m.Init == "openrc" {
		return "/etc/init.d/" + serviceName(m.Config.Instance) + "-update"
	}
	return "/etc/systemd/system/" + serviceName(m.Config.Instance) + "-update.service"
}
func agentService(m *manifest, action string) error {
	if m.Config.UpdateKey == "" {
		return nil
	}
	if m.Init == "openrc" {
		return command("/sbin/rc-service", serviceName(m.Config.Instance)+"-update", action)
	}
	return command("/usr/bin/systemctl", action, serviceName(m.Config.Instance)+"-update.service")
}
func agentEnable(m *manifest, on bool) error {
	if m.Config.UpdateKey == "" {
		return nil
	}
	a := "enable"
	if !on {
		a = "disable"
	}
	if m.Init == "openrc" {
		a = "add"
		if !on {
			a = "del"
			if _, e := os.Lstat("/etc/runlevels/default/" + serviceName(m.Config.Instance) + "-update"); os.IsNotExist(e) {
				return nil
			}
		}
		return command("/sbin/rc-update", a, serviceName(m.Config.Instance)+"-update", "default")
	}
	return command("/usr/bin/systemctl", a, serviceName(m.Config.Instance)+"-update.service")
}
func agentUnit(m *manifest) string {
	root := base(m.Config.Instance)
	name := serviceName(m.Config.Instance) + "-update"
	if m.Init == "openrc" {
		return fmt.Sprintf("#!/sbin/openrc-run\nname=%s\nsupervisor=supervise-daemon\ncommand=%s/filepanel\ncommand_args=\"update-agent --instance %s\"\npidfile=/run/%s.pid\nrespawn_delay=3\nrespawn_max=5\nrespawn_period=60\nexport GOMEMLIMIT=32MiB\nexport GOGC=50\ndepend() { need localmount; }\n", name, root, m.Config.Instance, name)
	}
	return fmt.Sprintf("[Unit]\nDescription=FilePanel fixed update agent %s\nAfter=local-fs.target\n[Service]\nType=simple\nExecStart=%s/filepanel update-agent --instance %s\nRestart=on-failure\nRestartSec=3\nUMask=0077\nEnvironment=GOMEMLIMIT=32MiB GOGC=50\nStandardOutput=null\nStandardError=null\n[Install]\nWantedBy=multi-user.target\n", m.Config.Instance, root, m.Config.Instance)
}
func putAgent(m *manifest) error {
	if m.Config.UpdateKey == "" {
		return nil
	}
	mode := os.FileMode(0644)
	if m.Init == "openrc" {
		mode = 0755
	}
	return put(agentPath(m), []byte(agentUnit(m)), mode)
}
