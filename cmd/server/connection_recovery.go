package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const connectionRecoveryGrace = 30 * time.Second

// Only a recent real disconnection receives a grace period. Long-offline and
// never-connected devices still fail promptly instead of tying up requests.
func (a *app) mappingRecoveryGrace(deviceID string, now time.Time) time.Duration {
	for _, d := range a.store.Snapshot().Devices {
		if d.ID != deviceID || d.LastDisconnectedAt.IsZero() {
			continue
		}
		age := now.Sub(d.LastDisconnectedAt)
		if age >= 0 && age < connectionRecoveryGrace {
			return connectionRecoveryGrace - age
		}
	}
	return 0
}

// A navigation whose in-flight tunnel died gets a neutral recovery view.
// Only HEAD is probed and only a GET navigation is repeated. Never replay a
// submitted form, upload or other operation whose result is uncertain.
func (a *app) mappingRecoveryPage(w http.ResponseWriter, r *http.Request, deviceID string) bool {
	grace := a.mappingRecoveryGrace(deviceID, time.Now())
	if grace <= 0 || r.Method != http.MethodGet || !strings.Contains(r.Header.Get("Accept"), "text/html") || (r.Header.Get("Sec-Fetch-Dest") != "" && r.Header.Get("Sec-Fetch-Dest") != "document") {
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "2")
	w.Header().Set("X-RemoteGate-Recovering", "1")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'self'")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>正在恢复连接 · RemoteGate</title><style>body{margin:0;background:#f5f7fc;color:#243753;font:16px/1.8 system-ui,sans-serif;display:grid;min-height:100vh;place-items:center}main{background:white;border:1px solid #dfe6f3;border-radius:20px;padding:32px;max-width:480px;margin:24px}h1{font-size:24px}p{color:#71839d}button{border:0;border-radius:10px;background:#4667eb;color:white;padding:12px 20px;cursor:pointer}.dot{display:inline-block;width:12px;height:12px;border-radius:50%%;background:#5473ee;animation:pulse 1.5s infinite}@keyframes pulse{50%%{opacity:.3}}</style><main><span class="dot" id="dot"></span><h1 id="heading">正在恢复连接</h1><p id="message">设备网络短暂波动，正在等待隧道恢复。请稍候，无需重复操作。</p><button id="retry" hidden>重新连接</button></main><script>(()=>{const end=Date.now()+%d;let stopped=false,current;function failed(){stopped=true;current?.abort();document.getElementById('dot').hidden=true;document.getElementById('heading').textContent='连接尚未恢复';document.getElementById('message').textContent='已等待网络恢复，请检查设备网络后重试。';document.getElementById('retry').hidden=false}document.getElementById('retry').onclick=()=>location.reload();const limit=setTimeout(failed,Math.max(0,end-Date.now()));async function probe(){if(stopped)return;current=new AbortController();const timer=setTimeout(()=>current.abort(),Math.min(8000,Math.max(1,end-Date.now())));try{const r=await fetch(location.href,{method:'HEAD',cache:'no-store',credentials:'same-origin',redirect:'manual',signal:current.signal});if(!stopped&&r.headers.get('X-RemoteGate-Recovering')!=='1'&&![502,503,504].includes(r.status)){stopped=true;clearTimeout(limit);location.replace(location.href);return}}catch(e){}finally{clearTimeout(timer)}if(!stopped)setTimeout(probe,2000)}setTimeout(probe,1500);window.addEventListener('pagehide',()=>{stopped=true;current?.abort();clearTimeout(limit)})})();</script></html>`, grace.Milliseconds())
	return true
}
