package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/local/remotegate/internal/config"
)

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
