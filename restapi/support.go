package restapi

import (
	"EtherCAT/constants"
	"EtherCAT/helper"
	"encoding/json"
	"io/ioutil"
	"net/http"
)

type Support struct {
	Sales           Contact `json:"sales"`
	Service         Contact `json:"service"`
	Version         string  `json:"version"`
	SoftwareVersion string  `json:"software_version"`
}

type Contact struct {
	ContactNumber string `json:"contact"`
	Email         string `json:"email"`
}

func getSupport(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)

	if r.Method == "OPTIONS" {
		return
	}

	if r.Method == "GET" {
		suprt, err := parseSupport()
		if err != nil {
			http.Error(
				w,
				"failed to load support configuration",
				http.StatusInternalServerError,
			)
			return
		}

		// SOFTWARE VERSION always comes from constants/constants.go
		suprt.SoftwareVersion = constants.SystemVersion

		w.Header().Set("Content-Type", "application/json")

		if err := json.NewEncoder(w).Encode(suprt); err != nil {
			http.Error(
				w,
				"failed to encode support response",
				http.StatusInternalServerError,
			)
			return
		}
	}
}

func parseSupport() (Support, error) {
	var suprt Support

	path := helper.AppendWDPath("/configs/support.json")

	aboutFile, err := ioutil.ReadFile(path)
	if err != nil {
		return suprt, err
	}

	if err := json.Unmarshal(aboutFile, &suprt); err != nil {
		return suprt, err
	}

	return suprt, nil
}