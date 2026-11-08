package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/local/remotegate/internal/config"
)

func (a *app) globalConnectionLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, a.store.ConnectionLimitDefaults())
		return
	}
	var input struct {
		Limits     config.ConnectionLimits `json:"limits"`
		ApplyToAll bool                    `json:"applyToAll"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "全局连接设置格式无效", http.StatusBadRequest)
		return
	}
	if err := input.Limits.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := a.store.SetConnectionLimitDefaults(input.Limits, input.ApplyToAll)
	if err != nil {
		http.Error(w, "全局连接设置保存失败，原有设置已保留，请检查数据目录后重试", http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (a *app) deviceConnectionLimits(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, exists := a.store.DeviceConnectionEvents(id); !exists {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPut {
		var limits config.ConnectionLimits
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&limits) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "连接设置格式无效", http.StatusBadRequest)
			return
		}
		if err := limits.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := a.store.SetDeviceConnectionLimits(id, limits); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
			} else {
				http.Error(w, "连接设置保存失败，请检查数据目录后重试", http.StatusInternalServerError)
			}
			return
		}
	}
	status := a.hub.Statuses()[id]
	writeJSON(w, map[string]any{"limits": a.store.DeviceConnectionLimits(id), "connections": status.Connections})
}
