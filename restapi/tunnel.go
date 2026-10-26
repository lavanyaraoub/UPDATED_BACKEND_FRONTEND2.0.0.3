package restapi

import (
	"EtherCAT/logger"
	"EtherCAT/tunnel"
	"encoding/json"
	"net/http"
)

func startHTTPTunnel(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)

	if r.Method == http.MethodOptions {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	t := tunnel.NewTunnel()
	logger.Debug("remote HTTP tunnel starting...")

	url, err := t.StartHTTP()

	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		logger.Error(err)
		resp := TextResponse{
			Status:   "error",
			Response: err.Error(),
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp := TextResponse{
		Status:   "success",
		Response: url,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func stopTunnel(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)

	if r.Method == http.MethodOptions {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	t := tunnel.NewTunnel()
	t.Stop()

	w.Header().Set("Content-Type", "application/json")
	resp := TextResponse{
		Status:   "success",
		Response: "Remote access tunnel stopped",
	}
	_ = json.NewEncoder(w).Encode(resp)
}
