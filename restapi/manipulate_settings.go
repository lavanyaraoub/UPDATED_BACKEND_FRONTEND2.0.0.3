package restapi

import (
	"EtherCAT/channels"
	"EtherCAT/executors"
	"EtherCAT/helper"
	"EtherCAT/logger"
	"EtherCAT/settings"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
)

// SettingsResponse struct to hold the settings data for the ui
type SettingsResponse struct {
	Status   string          `json:"status"`
	Response json.RawMessage `json:"resp,omitempty"`
	Error    string          `json:"error,omitempty"`
}

func manipulateSettings(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)

	if r.Method == http.MethodOptions {
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		settingsBytes, err := readSettingsFile()
		if err != nil {
			logger.Error("settings GET failed:", err)
			_ = json.NewEncoder(w).Encode(SettingsResponse{
				Status: "error",
				Error:  err.Error(),
			})
			return
		}

		resp := SettingsResponse{
			Status:   "success",
			Response: json.RawMessage(settingsBytes),
		}

		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid settings body", http.StatusBadRequest)
		return
	}

	// =====================================================
	// RS232 / ECS SAFETY INTERLOCK - OPTION A
	//
	// RS232 ON requires ECS = 1.
	// If RS232 is active, operator is NOT allowed to save ECS = 0.
	// The machine tells the operator exactly what to do.
	//
	// IMPORTANT:
	// We reject BEFORE settings.json is written.
	// So ECS remains unchanged.
	// =====================================================
	if executors.RS232Enabled.Load() {
		if ecs, ok := requestedECSForDrive(body, "A"); ok && ecs != 1 {
			const msg = "Cannot set ECS = 0 while RS232 is active. Turn RS232 OFF first."

			logger.Warn("[RS232-SAFETY] ECS disable rejected because RS232 is active")

			// Existing UI alarm path.
			// Frontend MachineParameters.js patch also catches this message and
			// shows it on the Machine Parameters screen itself.
			channels.SendAlarm(msg)

			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(SettingsResponse{
				Status: "error",
				Error:  msg,
			})
			return
		}
	}

	// Preserve existing write logic.
	jsonRaw := json.RawMessage(body)

	toWriteJSON, err := json.MarshalIndent(jsonRaw, "", "\t")
	if err != nil {
		logger.Error("settings marshal failed:", err)
		_ = json.NewEncoder(w).Encode(SettingsResponse{
			Status: "error",
			Error:  "invalid settings json",
		})
		return
	}

	errWrite := ioutil.WriteFile(helper.AppendWDPath("/settings/settings.json"), toWriteJSON, 0777)
	if errWrite != nil {
		logger.Error("settings write failed:", errWrite)
		_ = json.NewEncoder(w).Encode(SettingsResponse{
			Status: "error",
			Error:  errWrite.Error(),
		})
		return
	}

	// Reload the changed settings file, so all other modules get updated settings.
	settings.LoadDriverSettings()
	logger.Debug("settings updated...")

	_ = json.NewEncoder(w).Encode(SettingsResponse{
		Status: "success",
	})
}

func readSettingsFile() ([]byte, error) {
	jsonFile, err := os.Open(helper.AppendWDPath("/settings/settings.json"))
	if err != nil {
		return nil, err
	}
	defer jsonFile.Close()

	byteValue, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		return nil, err
	}

	return byteValue, nil
}

// requestedECSForDrive reads the ECS value from the incoming settings JSON
// without modifying the settings file.
func requestedECSForDrive(body []byte, driveName string) (int, bool) {
	var root map[string]json.RawMessage

	if err := json.Unmarshal(body, &root); err != nil {
		return 0, false
	}

	driveRaw, ok := root[driveName]
	if !ok {
		return 0, false
	}

	var drive map[string]json.RawMessage
	if err := json.Unmarshal(driveRaw, &drive); err != nil {
		return 0, false
	}

	ecsRaw, ok := drive["ecs"]
	if !ok {
		return 0, false
	}

	// Normal format: "ecs": 0 or "ecs": 1.
	var ecs int
	if err := json.Unmarshal(ecsRaw, &ecs); err == nil {
		return ecs, true
	}

	// Also tolerate string values: "ecs": "0" or "ecs": "1".
	var ecsString string
	if err := json.Unmarshal(ecsRaw, &ecsString); err == nil {
		switch ecsString {
		case "0":
			return 0, true
		case "1":
			return 1, true
		}
	}

	return 0, false
}
