package restapi

import (
	"EtherCAT/logger"
	"bufio"
	"encoding/json"
	"net/http"
	"os"
)

//ProgramFileContent return the program file content
type ProgramFileContent struct {
	Contents string `json:"contents"`
}

func getProgramFileContent(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}

	if r.Method != "GET" {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fileNames := r.URL.Query()["file_name"]
	if len(fileNames) <= 0 {
		http.Error(w, "file_name request parameter not exist.", http.StatusNotFound)
		return
	}
	logger.Trace("getting file content of ", fileNames[0])
	codeFileContent, err := readProgramFile(getCodeFilePath() + "/" + fileNames[0])
	if err != nil {
		logger.Error(err)
		http.Error(w, "file not found: "+fileNames[0], http.StatusNotFound)
		return
	}
	programFileContent := ProgramFileContent{Contents: codeFileContent}
	json.NewEncoder(w).Encode(programFileContent)
}

func readProgramFile(fileName string) (string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanLines)
	var txtlines []string

	for scanner.Scan() {
		txtlines = append(txtlines, scanner.Text())
	}
	var appendedLines string
	for _, lin := range txtlines {
		appendedLines = appendedLines + lin + "<br>"
	}
	return appendedLines, nil
}
