package restapi

import (
	"EtherCAT/channels"
	"EtherCAT/logger"
	"EtherCAT/settings"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type rs232StateResponse struct {
	Status  string `json:"status"`
	Enabled int    `json:"enabled"`
	Error   string `json:"error,omitempty"`
}

func rs232State(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		return
	}

	switch r.Method {
	case http.MethodGet:
		data, err := settings.LoadRS232Data()
		if err != nil {
			logger.Error("RS232 GET failed:", err)
			_ = json.NewEncoder(w).Encode(rs232StateResponse{
				Status:  "error",
				Enabled: 0,
				Error:   err.Error(),
			})
			return
		}

		enabled := settings.RS232DataToBool(data)
		_ = json.NewEncoder(w).Encode(rs232StateResponse{
			Status:  "success",
			Enabled: boolToInt(enabled),
		})

	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			logger.Error("RS232 POST read failed:", err)
			_ = json.NewEncoder(w).Encode(rs232StateResponse{
				Status:  "error",
				Enabled: 0,
				Error:   "invalid body",
			})
			return
		}

		enabled, ok := parseRS232EnabledPayload(body)
		if !ok {
			_ = json.NewEncoder(w).Encode(rs232StateResponse{
				Status:  "error",
				Enabled: 0,
				Error:   "enabled is required",
			})
			return
		}

		// =====================================================
		// RS232 / ECS SAFETY INTERLOCK - OPTION A
		//
		// REST endpoint also must not persist RS232=ON while ECS=OFF.
		// =====================================================
		if enabled && settings.GetDriverSettings("A").ECS != 1 {
			const msg = "Cannot enable RS232. ECS is OFF. Set ECS = 1 and press SAVE."

			logger.Warn("[RS232-SAFETY] REST RS232 enable rejected because ECS is disabled")

			// Existing global alarm path; Machine Parameters frontend patch also
			// shows this on the settings screen itself.
			channels.SendAlarm(msg)

			// Fail safe: keep persisted state OFF.
			if err := settings.SaveRS232Data("0"); err != nil {
				logger.Error("[RS232-SAFETY] Failed to persist RS232 OFF:", err)
			}

			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(rs232StateResponse{
				Status:  "error",
				Enabled: 0,
				Error:   msg,
			})
			return
		}

		// Persist using your existing logic (Data: "0"/"1").
		if err := settings.SaveRS232Data(settings.RS232BoolToData(enabled)); err != nil {
			logger.Error("RS232 POST persist failed:", err)
			_ = json.NewEncoder(w).Encode(rs232StateResponse{
				Status:  "error",
				Enabled: boolToInt(enabled),
				Error:   err.Error(),
			})
			return
		}

		// NOTE: We intentionally do NOT call serialtest.ApplyRS232State here,
		// because this endpoint preserves previous working logic and only
		// persists state/reports it.
		_ = json.NewEncoder(w).Encode(rs232StateResponse{
			Status:  "success",
			Enabled: boolToInt(enabled),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(rs232StateResponse{
			Status:  "error",
			Enabled: 0,
			Error:   "method not allowed",
		})
	}
}

func parseRS232EnabledPayload(body []byte) (bool, bool) {
	if len(body) == 0 {
		return false, false
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, false
	}

	// Accept multiple keys (enabled / Enabled / data / Data).
	if v, ok := payload["enabled"]; ok {
		return parseRS232EnabledValue(v)
	}
	if v, ok := payload["Enabled"]; ok {
		return parseRS232EnabledValue(v)
	}
	if v, ok := payload["data"]; ok {
		return parseRS232EnabledValue(v)
	}
	if v, ok := payload["Data"]; ok {
		return parseRS232EnabledValue(v)
	}

	return false, false
}

func parseRS232EnabledValue(v interface{}) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case float64:
		return t != 0, true
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		if s == "1" || s == "true" || s == "on" || s == "enabled" {
			return true, true
		}
		if s == "0" || s == "false" || s == "off" || s == "disabled" {
			return false, true
		}
	}

	return false, false
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
