package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	//"time"
)

const rs232StateFile = "/mnt/app/jamun/settings/rs232.json"

type rs232State struct {
	Data      string    `json:"Data"`       // "0" or "1" (matches your frontend contract)
	//UpdatedAt time.Time `json:"updated_at"` // optional
}

// LoadRS232Data returns "0" or "1". If file doesn't exist, returns "0".
func LoadRS232Data() (string, error) {
	b, err := os.ReadFile(rs232StateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "0", nil
		}
		return "0", err
	}

	var st rs232State
	if err := json.Unmarshal(b, &st); err != nil {
		return "0", err
	}
	if st.Data != "0" && st.Data != "1" {
		return "0", nil
	}
	return st.Data, nil
}

// SaveRS232Data persists "0" or "1" atomically.
func SaveRS232Data(data string) error {
	if data != "0" && data != "1" {
		data = "0"
	}

	dir := filepath.Dir(rs232StateFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	st := rs232State{
		Data:      data,
		//UpdatedAt: time.Now(),
	}

	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}

	tmp := rs232StateFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, rs232StateFile)
}

// Helpers if you prefer bool in executor code
func RS232DataToBool(data string) bool { return data == "1" }
func RS232BoolToData(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
