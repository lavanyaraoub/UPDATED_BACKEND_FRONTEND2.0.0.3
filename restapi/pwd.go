package restapi

import (
	"EtherCAT/helper"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
)

func readPwd(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}
	if (*r).Method == "GET" {
		code, _ := readPwdFile()
		resp := SettingsResponse{Status: "success", Response: json.RawMessage(code)}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func readPwdFile() ([]byte, error) {
	jsonFile, err := os.Open(helper.AppendWDPath("/configs/code.json"))
	if err != nil {
		return nil, err
	}
	defer jsonFile.Close()
	byteValue, _ := ioutil.ReadAll(jsonFile)
	return byteValue, nil
}
