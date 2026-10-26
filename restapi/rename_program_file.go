package restapi

import (
	"encoding/json"
	"net/http"
	"os"
)

func renameProgramFile(w http.ResponseWriter, r *http.Request) {
	setupCorsResponse(&w, r)
	if (*r).Method == "OPTIONS" {
		return
	}
	fileNames := r.URL.Query()["file_name"]
	if len(fileNames) <= 0 {
		http.Error(w, "file_name request parameter not exist.", http.StatusNotFound)
		return
	}

	newFileNames := r.URL.Query()["new_file_name"]
	if len(newFileNames) <= 0 {
		http.Error(w, "new_file_name request parameter not exist.", http.StatusNotFound)
		return
	}

	err := os.Rename(getCodeFilePath()+"/"+fileNames[0], getCodeFilePath()+"/"+newFileNames[0])
	if err != nil {
		json.NewEncoder(w).Encode("{\"status\":\"error\"}")
	} else {
		json.NewEncoder(w).Encode("{\"status\":\"Success\"}")
	}
}
