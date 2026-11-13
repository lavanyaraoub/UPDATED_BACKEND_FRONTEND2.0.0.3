package restapi

import (
	"EtherCAT/logger"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
)

//ProgramFiles return the files as an array
//json format will be { files: ["program1", "program2"]}
type ProgramFiles struct {
	Files []string `json:"files"`
}

func getProgramFiles(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}

	if r.URL.Path != "/programs" {
		http.Error(w, "404 not found.", http.StatusNotFound)
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	codeFiles := getAllFiles()
	programFiles := ProgramFiles{Files: codeFiles}
	json.NewEncoder(w).Encode(programFiles)
}

func getAllFiles() []string {
	var fileNames []string
	if _, err := os.Stat(getCodeFilePath()); os.IsNotExist(err) {
		os.Mkdir(getCodeFilePath(), os.ModePerm)
	}
	files, err := ioutil.ReadDir(getCodeFilePath())
	if err != nil {
		logger.Fatal(err)
	}
	for _, f := range files {
		fileNames = append(fileNames, f.Name())
	}
	return fileNames
}
