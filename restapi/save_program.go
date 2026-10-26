package restapi

import (
	"EtherCAT/executors"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
)

// NewProgram holds the code for the new program
type NewProgram struct {
	FileName string `json:"file_name"`
	Code     string `json:"contents"`
}

func saveProgram(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}
	var program NewProgram
	err := json.NewDecoder(r.Body).Decode(&program)
	if err != nil {
		http.Error(w, "unable to save the program", http.StatusNotFound)
		return
	}

	content := []byte(program.Code)
	fileName := getCodeFilePath() + "/" + program.FileName
	err = ioutil.WriteFile(fileName, content, 0777)
	if err == nil {
		err = executors.CompileProgram(fileName)
	}
	if err != nil {
		os.Remove(fileName)
		json.NewEncoder(w).Encode(fmt.Sprintf("{\"status\":\"error\", \"desc\":\"%s\"}", err.Error()))
	} else {
		// A save is an edit/replacement of the program contents. Any persisted
		// resume point from a previous run of the same filename is now stale
		// (the file may now have fewer lines, or different content at that
		// line entirely) — clear it so the next Run starts from line 0 unless
		// the UI explicitly selects a start line again.
		executors.ClearExecutionResumeState()
		json.NewEncoder(w).Encode("{\"status\":\"success\"}")
	}
}
