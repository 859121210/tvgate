package monitor

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/qist/tvgate/config"
)

// 页面数据结构
type StatusData struct {
	Timestamp     time.Time
	Uptime        time.Duration
	Version       string
	Goroutines    int
	MemoryStats   runtime.MemStats
	ProxyGroups   map[string]*config.ProxyGroupConfig
	TrafficStats  *TrafficStats
	ClientIP      string
	ActiveClients []*ClientConnection
}

// HTTP 处理入口
func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("server", "TVGate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Header.Get("Accept") == "application/json" || r.URL.Query().Get("format") == "json" {
		handleJSONRequest(w, r)
		return
	}
	handleHTMLRequest(w, r)
}

func handleJSONRequest(w http.ResponseWriter, r *http.Request) {
	data := prepareStatusData(r)
	w.Header().Set("server", "TVGate")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func handleHTMLRequest(w http.ResponseWriter, r *http.Request) {
	data := prepareStatusData(r)

	tmpl := `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>TVGate 状态监控</title>
<style>
body { font-family: 'Segoe UI', sans-serif; max-width:1200px;margin:20px auto;background:#f5f5f5; color:#333;}
.header {background:#667eea; color:white; padding:20px; border-radius:10px;margin-bottom:20px;box-shadow:0 2px 8px rgba(0,0,0,0.15);}
.header h1 {margin:0;}
.table {width:100%; border-collapse: collapse; margin-bottom:20px; table-layout:fixed; word-wrap:break-word;}
.table th, .table td {border:1px solid #ddd; padding:8px; text-align:left; max-width:200px; white-space:nowrap; overflow:hidden; text-overflow:ellipsis;}
.table th {background:#f9f9f9;}
.table tr:nth-child(even) {background:#fafafa;}
.table tr:hover {background:#f1f1f1;}
.table td.url-cell {max-width:700px;}
.table td.ua-cell {max-width:200px;}
.status-alive {color:#4CAF50;font-weight:bold;}
.status-dead {color:#f44336;font-weight:bold;}
.status-cooldown {color:#ff9800;font-weight:bold;}
.status-unknown {color:#9E9E9E;font-weight:bold;}
.refresh-controls {margin:10px 0 20px; display:flex; align-items:center; gap:10px;}
.refresh-btn {border:none; padding:8px 15px; border-radius:5px; font-weight:bold; cursor:pointer;}
.refresh-on {background:#4CAF50; color:white;}
.refresh-off {background:#f44336; color:white;}
.toggle-column {cursor:pointer; user-select:none;}
</style>
</head>
<body>

<div class="header">
<h1>TVGate 状态监控</h1>
<p>更新时间: {{.Timestamp.Format "2006-01-02 15:04:05"}}</p>
</div>

<div class="refresh-controls">
<button id="toggleRefresh" class="refresh-btn">⟳ 自动刷新</button>
<label for="interval">间隔:</label>
<select id="interval">
<option value="1000">1s</option>
<option value="3000">3s</option>
<option value="5000">5s</option>
<option value="10000">10s</option>
<option value="30000">30s</option>
</select>
</div>

<h2>系统信息</h2>
<div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 15px; margin-bottom: 20px;">
  <div style="background: #f8f9fa; padding: 15px; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #495057;">基础信息</h3>
    <ul style="list-style: none; padding: 0;">
      <li style="padding: 5px 0;"><strong>操作系统:</strong> {{.TrafficStats.HostInfo.Platform}}</li>
      <li style="padding: 5px 0;"><strong>内核版本:</strong> {{.TrafficStats.HostInfo.KernelVersion}}</li>
      <li style="padding: 5px 0;"><strong>CPU架构:</strong> {{.TrafficStats.HostInfo.KernelArch}}</li>
      <li style="padding: 5px 0;"><strong>版本:</strong> {{.Version}}</li>
      <li style="padding: 5px 0;"><strong>运行时间:</strong> 
        {{$totalSeconds := .Uptime.Seconds}}
        {{$days := float64ToInt64 (divFloat64 $totalSeconds 86400)}}
        {{$hours := float64ToInt64 (divFloat64 (modFloat64 $totalSeconds 86400) 3600)}}
        {{$minutes := float64ToInt64 (divFloat64 (modFloat64 $totalSeconds 3600) 60)}}
        {{$seconds := float64ToInt64 (modFloat64 $totalSeconds 60)}}
        {{if gt $days 0}}{{$days}}天{{end}}{{if gt $hours 0}}{{$hours}}小时{{end}}{{if gt $minutes 0}}{{$minutes}}分{{end}}{{$seconds}}秒
      </li>
      <li style="padding: 5px 0;"><strong>Goroutines:</strong> {{.Goroutines}}</li>
      <li style="padding: 5px 0;"><strong>客户端IP:</strong> {{.ClientIP}}</li>
    </ul>
  </div>
  
  <div style="background: #f8f9fa; padding: 15px; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #495057;">网络流量</h3>
    <ul style="list-style: none; padding: 0;">
      <li style="padding: 5px 0;"><strong>总流量:</strong> {{FormatBytes .TrafficStats.TotalBytes}}</li>
      <li style="padding: 5px 0;"><strong>入口流量:</strong> {{FormatBytes .TrafficStats.InboundBytes}}</li>
      <li style="padding: 5px 0;"><strong>出口流量:</strong> {{FormatBytes .TrafficStats.OutboundBytes}}</li>
      <li style="padding: 5px 0;"><strong>实时总入带宽:</strong> {{FormatNetworkBandwidth .TrafficStats.InboundBandwidth}}</li>
      <li style="padding: 5px 0;"><strong>实时总出带宽:</strong> {{FormatNetworkBandwidth .TrafficStats.OutboundBandwidth}}</li>
    </ul>
  </div>
  
  <div style="background: #f8f9fa; padding: 15px; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #495057;">CPU与内存</h3>
    <ul style="list-style: none; padding: 0;">
      <li style="padding: 5px 0;"><strong>系统负载:</strong> {{printf "%.2f" .TrafficStats.LoadAverage.Load1}} / {{printf "%.2f" .TrafficStats.LoadAverage.Load5}} / {{printf "%.2f" .TrafficStats.LoadAverage.Load15}}</li>
      <li style="padding: 5px 0;"><strong>CPU核心数:</strong> {{.TrafficStats.CPUCount}}</li> 
	  <li style="padding: 5px 0;"><strong>CPU 使用率:</strong> {{printf "%.2f%%" .TrafficStats.CPUUsage}}</li>
      <li style="padding: 5px 0;"><strong>内存使用:</strong> {{FormatBytes .TrafficStats.MemoryUsage}} / {{FormatBytes .TrafficStats.MemoryTotal}}</li>
    </ul>
  </div>
</div>

<div style="display: grid; grid-template-columns: 1fr 1fr; gap: 15px; margin-bottom: 20px;">
  <div style="background: #f8f9fa; padding: 15px; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #495057;">存储信息</h3>
    {{if .TrafficStats.DiskPartitions}}
    <table style="width: 100%; border-collapse: collapse;">
      <thead>
        <tr style="background-color: #e9ecef;">
          <th style="text-align: left; padding: 8px; border: 1px solid #dee2e6; width: 20%;">挂载点</th>
          <th style="text-align: left; padding: 8px; border: 1px solid #dee2e6; width: 15%;">文件系统</th>
          <th style="text-align: left; padding: 8px; border: 1px solid #dee2e6; width: 35%;">已用/总量</th>
          <th style="text-align: left; padding: 8px; border: 1px solid #dee2e6; width: 30%;">使用率</th>
        </tr>
      </thead>
      <tbody>
        {{range .TrafficStats.DiskPartitions}}
        <tr>
          <td style="padding: 8px; border: 1px solid #dee2e6;">{{.MountPoint}}</td>
          <td style="padding: 8px; border: 1px solid #dee2e6;">{{.FsType}}</td>
          <td style="padding: 8px; border: 1px solid #dee2e6;">{{FormatBytes .Used}} / {{FormatBytes .Total}}</td>
          <td style="padding: 8px; border: 1px solid #dee2e6;">
            <div style="display: flex; align-items: center;">
              <div style="width: 100%; background-color: #e9ecef; border-radius: 4px; height: 16px; margin-right: 8px;">
                <div style="width: {{if gt .UsedPercent 100.0}}100{{else if lt .UsedPercent 0.0}}0{{else}}{{printf "%.0f" .UsedPercent}}{{end}}%; height: 16px; background-color: {{if gt .UsedPercent 90.0}}#dc3545{{else if gt .UsedPercent 75.0}}#ffc107{{else}}#28a745{{end}}; border-radius: 4px;"></div>
              </div>
              <span>{{printf "%.2f%%" .UsedPercent}}</span>
            </div>
          </td>
        </tr>
        {{end}}
      </tbody>
    </table>
    {{end}}
  </div>
  
  <div style="background: #f8f9fa; padding: 15px; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.1);">
    <h3 style="margin-top: 0; color: #495057;">各网卡流量详情</h3>
    {{if .TrafficStats.NetworkInterfaces}}
    <table style="width: 100%; border-collapse: collapse; font-size: 0.9em;">
      <thead>
        <tr style="background-color: #e9ecef;">
          <th style="text-align: left; padding: 6px; border: 1px solid #dee2e6;">网卡</th>
          <th style="text-align: left; padding: 6px; border: 1px solid #dee2e6;">接收</th>
          <th style="text-align: left; padding: 6px; border: 1px solid #dee2e6;">发送</th>
          <th style="text-align: left; padding: 6px; border: 1px solid #dee2e6;">接收带宽</th>
          <th style="text-align: left; padding: 6px; border: 1px solid #dee2e6;">发送带宽</th>
        </tr>
      </thead>
      <tbody>
        {{range .TrafficStats.NetworkInterfaces}}
        <tr>
          <td style="padding: 6px; border: 1px solid #dee2e6;">{{.Name}}</td>
          <td style="padding: 6px; border: 1px solid #dee2e6;">{{FormatBytes .BytesRecv}}</td>
          <td style="padding: 6px; border: 1px solid #dee2e6;">{{FormatBytes .BytesSent}}</td>
          <td style="padding: 6px; border: 1px solid #dee2e6;">{{FormatNetworkBandwidth .RecvBandwidth}}</td>
          <td style="padding: 6px; border: 1px solid #dee2e6;">{{FormatNetworkBandwidth .SendBandwidth}}</td>
        </tr>
        {{end}}
      </tbody>
    </table>
    {{end}}
  </div>
</div>

<h2>活跃客户端连接</h2>
<table class="table">
<tr>
<th style="width:250px;">IP</th>
<th style="width:570px;">URL</th>
<th style="width:50px;">类型</th>
<th style="width:100px;">UA</th>
<th style="width:100px; text-align:center;">连接时间</th>
<th style="width:100px; text-align:center;">最后活跃</th>
</tr>
{{range .ActiveClients}}
<tr>
<td>{{.IP}}</td>
<td class="url-cell" title="{{.URL}}">{{.URL}}</td>
<td>{{.ConnectionType}}</td>
<td class="ua-cell" title="{{.UserAgent}}">{{.UserAgent}}</td>
<td style="text-align:center;">{{.ConnectedAt.Format "15:04:05"}}</td>
<td style="text-align:center;">{{.LastActive.Format "15:04:05"}}</td>
</tr>
{{end}}
</table>

<h2>代理组状态</h2>
{{range $name, $group := .ProxyGroups}}
<h3>{{$name}} (负载均衡: {{$group.LoadBalance}})</h3>
<table class="table">
<tr>
<th>代理</th>
<th>延迟 <span class="toggle-column" data-column="1" data-group="{{$name}}">👁</span></th>
<th>类型 <span class="toggle-column" data-column="2" data-group="{{$name}}">👁</span></th>
<th>服务器 <span class="toggle-column" data-column="3" data-group="{{$name}}">👁</span></th>
<th>状态</th>
</tr>
{{range $proxy := $group.Proxies}}
<tr>
<td>{{$proxy.Name}}</td>
<td data-column="1" data-group="{{$name}}" data-value="{{ $stats := index $group.Stats.ProxyStats $proxy.Name }}{{if $stats}}{{if gt $stats.ResponseTime 0}}{{printf "%.0f ms" (divInt64 $stats.ResponseTime.Nanoseconds 1000000)}}{{end}}{{end}}">*</td>
<td data-column="2" data-group="{{$name}}" data-value="{{if $proxy.Type}}{{$proxy.Type}}{{end}}">*</td>
<td data-column="3" data-group="{{$name}}" data-value="{{if $proxy.Server}}{{$proxy.Server}}{{end}}">*</td>
<td>
{{ $stats := index $group.Stats.ProxyStats $proxy.Name }}
{{if $stats}}
{{if $stats.Alive}}<span class="status-alive">✅ 活跃</span>
{{else if $stats.CooldownUntil.After $.Timestamp}}<span class="status-cooldown">🚫 冷却</span>
{{else if and (not $stats.Alive) (or (not (eq $stats.ResponseTime 0)) (gt $stats.FailCount 0))}}<span class="status-dead">❌ 死亡</span>
{{else}}<span class="status-unknown">⚪ 未测试</span>
{{end}}
{{else}}<span class="status-unknown">⚪ 未初始化</span>{{end}}
</td>
</tr>
{{end}}
</table>
{{end}}

<script>
let refreshMs = parseInt(localStorage.getItem('refreshMs')) || 3000;
let auto = localStorage.getItem('autoRefresh') !== 'false';
let timer = null;
const toggleBtn = document.getElementById('toggleRefresh');
const intervalSelect = document.getElementById('interval');
if(intervalSelect.querySelector('option[value="'+refreshMs+'"]')) intervalSelect.value = refreshMs;

function applyButtonUI(){
    if(auto){
        toggleBtn.textContent = '⟳ 自动刷新 (' + (refreshMs/1000) + 's)';
        toggleBtn.className = 'refresh-btn refresh-on';
    }else{
        toggleBtn.textContent = '⏸ 刷新已暂停';
        toggleBtn.className = 'refresh-btn refresh-off';
    }
}

function stopTimer(){ if(timer){ clearInterval(timer); timer=null; } }
function startTimer(){ stopTimer(); timer=setInterval(()=>{location.reload();}, refreshMs); }
function persist(){ localStorage.setItem('autoRefresh', auto); localStorage.setItem('refreshMs', refreshMs); }

if(auto) startTimer();
applyButtonUI();

// 每个组独立列显示/隐藏
document.querySelectorAll('.toggle-column').forEach(el => {
    el.addEventListener('click', function() {
        const colIndex = this.getAttribute('data-column');
        const group = this.getAttribute('data-group');
        const tds = document.querySelectorAll('td[data-column="'+colIndex+'"][data-group="'+group+'"]');
        tds.forEach(td => {
            const colIndex = td.getAttribute('data-column');
            const realValue = td.getAttribute('data-value'); // 真实值可能为空
            if(td.textContent === "*"){ // 当前是隐藏状态
                 td.textContent = realValue || ""; // 显示真实值，如果空保持空
            } else {
                 td.textContent = "*"; // 隐藏时用 * 替代
            }
        });
    });
});

toggleBtn.onclick=()=>{ auto=!auto; if(auto) startTimer(); else stopTimer(); applyButtonUI(); persist(); };
intervalSelect.onchange=()=>{ refreshMs=parseInt(intervalSelect.value); if(auto) startTimer(); applyButtonUI(); persist(); };










if(auto) startTimer();
applyButtonUI();


</script>

</body>
</html>`

	t, err := template.New("status").Funcs(template.FuncMap{
		"divInt64": func(a int64, b ...int64) float64 {
			result := float64(a)
			for _, v := range b {
				if v != 0 {
					result /= float64(v)
				}
			}
			return result
		},
		"modInt64": func(a int64, b int64) int64 {
			if b != 0 {
				return a % b
			}
			return 0
		},
		"divFloat64": func(a float64, b ...float64) float64 {
			result := a
			for _, v := range b {
				if v != 0 {
					result /= v
				}
			}
			return result
		},
		"modFloat64": func(a float64, b float64) float64 {
			if b != 0 {
				return float64(int64(a) % int64(b))
			}
			return 0
		},
		"float64ToInt64": func(a float64) int64 {
			return int64(a)
		},
		"FormatBytes":            FormatBytes,
		"FormatBytesPerSec":      FormatBytesPerSec,
		"FormatNetworkBandwidth": FormatNetworkBandwidth,
	}).Parse(tmpl)

	if err != nil {
		http.Error(w, "模板解析错误: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := t.Execute(w, data); err != nil {
		http.Error(w, "模板执行错误: "+err.Error(), http.StatusInternalServerError)
	}
}

// 字节格式化
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// 带宽格式化
func FormatBytesPerSec(bytes uint64, _ uint64) string {
	return FormatBytes(bytes) + "/s"
}

// 网络流量带宽格式化
func FormatNetworkBandwidth(bytes uint64) string {
	return FormatBytes(bytes) + "/s"
}

func prepareStatusData(r *http.Request) StatusData {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	clientIP := GetClientIP(r)

	config.CfgMu.RLock()
	proxyGroups := make(map[string]*config.ProxyGroupConfig)
	for name, group := range config.Cfg.ProxyGroups {
		groupCopy := &config.ProxyGroupConfig{
			Proxies:     make([]*config.ProxyConfig, len(group.Proxies)),
			Domains:     group.Domains,
			LoadBalance: group.LoadBalance,
			Stats:       &config.GroupStats{ProxyStats: make(map[string]*config.ProxyStats)},
		}
		for i, p := range group.Proxies {
			groupCopy.Proxies[i] = &config.ProxyConfig{
				Name: p.Name, Type: p.Type, Server: p.Server, Port: 0, UDP: p.UDP,
			}
			if group.Stats != nil && group.Stats.ProxyStats != nil {
				if stats, ok := group.Stats.ProxyStats[p.Name]; ok {
					groupCopy.Stats.ProxyStats[p.Name] = stats
				} else {
					groupCopy.Stats.ProxyStats[p.Name] = &config.ProxyStats{}
				}
			} else {
				groupCopy.Stats.ProxyStats[p.Name] = &config.ProxyStats{}
			}
		}
		proxyGroups[name] = groupCopy
	}
	config.CfgMu.RUnlock()

	return StatusData{
		Timestamp:     time.Now(),
		Uptime:        time.Since(config.StartTime),
		Version:       config.Version,
		Goroutines:    runtime.NumGoroutine(),
		MemoryStats:   memStats,
		ProxyGroups:   proxyGroups,
		TrafficStats:  GlobalTrafficStats.GetTrafficStats(),
		ClientIP:      clientIP,
		ActiveClients: ActiveClients.GetAll(),
	}
}

func GetClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
