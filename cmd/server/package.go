package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

func (a *app) packageInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	result := map[string]any{"available": false, "name": filepath.Base(installerPath())}
	data, err := os.ReadFile(installerPath())
	if err != nil {
		writeJSON(w, result)
		return
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	result["available"] = true
	result["size"] = len(data)
	result["sha256"] = digest
	result["download"] = "/downloads/remotegate.run"
	var manifest map[string]any
	raw, err := os.ReadFile(installerPath() + ".json")
	if err == nil && json.Unmarshal(raw, &manifest) == nil && manifest["sha256"] == digest {
		result["manifest"] = manifest
	}
	writeJSON(w, result)
}
