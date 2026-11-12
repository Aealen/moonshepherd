//go:build linux

package agentd

import (
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// collectHeartbeat Linux 心跳采集：/proc + df（docs/04 §5.1）。
// 任何一项失败即省略该字段（面板对缺失字段的策略：忽略并照常更新 last_seen）。
func collectHeartbeat() protocol.HeartbeatPayload {
	p := protocol.HeartbeatPayload{}
	p.CPUPercent, p.Load1 = cpuSnapshot()
	if mem, ok := memSnapshot(); ok {
		p.Mem = mem
	}
	if d, ok := diskSnapshot(); ok {
		p.Disk = d
	}
	p.Net = netSnapshot()
	p.UptimeS = uptime()
	return p
}

// cpu 快照：两次 /proc/stat 采样均值（间隔 200ms）+ loadavg。
func cpuSnapshot() (float64, float64) {
	readStat := func() (idle, total uint64) {
		b, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0
		}
		line := strings.SplitN(string(b), "\n", 2)[0]
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0
		}
		var vals [10]uint64
		for i := 1; i < len(fields) && i <= 10; i++ {
			v, _ := strconv.ParseUint(fields[i], 10, 64)
			vals[i-1] = v
		}
		idle = vals[3] + vals[4] // idle + iowait
		for _, v := range vals {
			total += v
		}
		return
	}
	i1, t1 := readStat()
	if t1 == 0 {
		return 0, 0
	}
	time.Sleep(200 * time.Millisecond)
	i2, t2 := readStat()
	if t2 <= t1 {
		return 0, 0
	}
	used := (t2 - t1) - (i2 - i1)
	percent := float64(used) / float64(t2-t1) * 100

	var load1 float64
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	return percent, load1
}

func memSnapshot() (*protocol.MemSnapshot, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, false
	}
	var total, avail int64
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			total = parseKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			avail = parseKB(line)
		}
	}
	if total == 0 {
		return nil, false
	}
	return &protocol.MemSnapshot{UsedMB: (total - avail) / 1024, TotalMB: total / 1024}, true
}

func parseKB(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(fields[1], 10, 64)
	return v
}

func diskSnapshot() (*protocol.DiskSnapshot, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return nil, false
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total == 0 {
		return nil, false
	}
	return &protocol.DiskSnapshot{
		Path: "/", TotalGB: int64(total / 1e9), UsedGB: int64((total - free) / 1e9),
	}, true
}

func uptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return int64(v)
}

// net 快照：两次 /proc/net/dev 采样差值折算 KB/s（全网卡聚合，docs/04 §5.1）。
// 基准是上一次心跳（约 30s 间隔，速率平滑）；首次心跳无基准返回 nil，面板按缺省容缺。
// 排除回环与宿主机上的容器虚拟接口（docker0/veth/br-），避免流量重复计入。
var lastNet struct {
	ts     time.Time
	rx, tx uint64
}

func netSnapshot() *protocol.NetSnapshot {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil
	}
	var rx, tx uint64
	for _, line := range strings.Split(string(b), "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "lo" || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") {
			continue
		}
		// /proc/net/dev 列序：rx bytes 在第 0 列，tx bytes 在第 8 列
		f := strings.Fields(line[idx+1:])
		if len(f) < 9 {
			continue
		}
		rxb, _ := strconv.ParseUint(f[0], 10, 64)
		txb, _ := strconv.ParseUint(f[8], 10, 64)
		rx += rxb
		tx += txb
	}
	now := time.Now()
	if lastNet.ts.IsZero() || !now.After(lastNet.ts) {
		lastNet.ts, lastNet.rx, lastNet.tx = now, rx, tx
		return nil
	}
	sec := now.Sub(lastNet.ts).Seconds()
	rxKBps := math.Max(0, float64(rx-lastNet.rx)/sec/1024) // 计数器回绕/接口重建时取 0
	txKBps := math.Max(0, float64(tx-lastNet.tx)/sec/1024)
	lastNet.ts, lastNet.rx, lastNet.tx = now, rx, tx
	return &protocol.NetSnapshot{RxKBps: rxKBps, TxKBps: txKBps}
}
