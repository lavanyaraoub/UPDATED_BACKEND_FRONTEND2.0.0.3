package restapi

import (
	"EtherCAT/helper"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
)

func readFaq(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}
	if (*r).Method == "GET" {
		settings, _ := readFaqFile()
		resp := SettingsResponse{Status: "success", Response: json.RawMessage(settings)}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func readFaqFile() ([]byte, error) {
	jsonFile, err := os.Open(helper.AppendWDPath("/configs/faq.json"))
	if err != nil {
		return nil, err
	}
	defer jsonFile.Close()
	byteValue, _ := ioutil.ReadAll(jsonFile)
	return byteValue, nil
}
