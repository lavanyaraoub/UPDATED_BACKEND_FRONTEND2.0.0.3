package webserver

import (
	"EtherCAT/helper"
	"EtherCAT/logger"
	"net/http"
)

type WebUI struct {
}

func NewWebUI() WebUI {
	return WebUI{}
}

//HostControllerUI will host the controller web ui
//can be accessed via http://<ipaddress>:8000
func (w WebUI) Start(uiVer string) {
	logger.Info("UI webserver starting at 8000")
	if uiVer == "v2" {
		http.Handle("/", http.FileServer(http.Dir(helper.AppendWDPath("/www_v2"))))
	} else {
		http.Handle("/", http.FileServer(http.Dir(helper.AppendWDPath("/www"))))
	}
	if err := http.ListenAndServe(":8000", nil); err != nil {
		logger.Fatal(err)
	}
}
