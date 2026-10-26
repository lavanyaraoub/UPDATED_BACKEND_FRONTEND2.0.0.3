package restapi

import (
	"EtherCAT/systemupdate"
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

func otaStatus(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	values := map[string]string{
		"state":          "IDLE",
		"progress":       "0",
		"safe_to_reboot": "yes",
		"message":        "No update is currently running. It is safe to reboot.",
	}

	if f, err := os.Open("/mnt/app/jamun/updates/status"); err == nil {
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			p := strings.SplitN(s.Text(), "=", 2)
			if len(p) == 2 {
				values[p[0]] = p[1]
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(values)
}

func rollbackCheck(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	backup, err := systemupdate.GetLatestBackup()
	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(TextResponse{Status: "error", Response: err.Error()})
		return
	}

	json.NewEncoder(w).Encode(struct {
		Status    string `json:"status"`
		Version   string `json:"version"`
		Timestamp string `json:"timestamp"`
	}{
		Status:    "success",
		Version:   backup.Version,
		Timestamp: backup.Timestamp,
	})
}

func rollbackApply(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	go systemupdate.PerformRollback(true)

	json.NewEncoder(w).Encode(TextResponse{
		Status:   "success",
		Response: "Rollback started. System will restart shortly.",
	})
}