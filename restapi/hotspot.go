package restapi

import (
	"EtherCAT/hotspot"
	"encoding/json"
	"net/http"
)

func createHotspot(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}
	if (*r).Method == "POST" {
		h := hotspot.NewHotspot()
		h.Create()
		resp := SettingsResponse{Status: "success"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func killHotspot(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}
	if (*r).Method == "POST" {
		h := hotspot.NewHotspot()
		h.Kill()
		resp := SettingsResponse{Status: "success"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

type WifiConfig struct {
	SSID    string `json:"ssid"`
	Pwd     string `json:"pwd"`
	Country string `json:"country"`
}

func configureWifi(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}
	if (*r).Method == "POST" {
		var wifi WifiConfig
		err := json.NewDecoder(r.Body).Decode(&wifi)
		if err != nil {
			http.Error(w, "unable to parse wifi configuration from client", http.StatusNotFound)
			return
		}
		if wifi.Country == "" {
			wifi.Country = "IN"
		}
		err = hotspot.ConfigureWifi(wifi.SSID, wifi.Pwd, wifi.Country)
		if err != nil {
			http.Error(w, "unable to modify wifi configuration", http.StatusNotFound)
			return
		}
		resp := SettingsResponse{Status: "success"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
