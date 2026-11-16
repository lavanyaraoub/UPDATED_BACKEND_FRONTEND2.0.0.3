package restapi

import (
	"encoding/json"
	"net/http"
	"os"
)

func deleteProgram(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}
	fileNames := r.URL.Query()["file_name"]
	if len(fileNames) <= 0 {
		http.Error(w, "file_name request parameter not exist.", http.StatusNotFound)
		return
	}

	err := os.Remove(getCodeFilePath() + "/" + fileNames[0])
	if err != nil {
		json.NewEncoder(w).Encode("{\"status\":\"error\"}")
	} else {
		json.NewEncoder(w).Encode("{\"status\":\"Success\"}")
	}
}
