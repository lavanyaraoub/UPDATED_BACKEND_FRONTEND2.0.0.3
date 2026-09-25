package helper

import (
	"os"
	"path/filepath"
)

func GetCodeFilePath() string {
	// path, _ := filepath.Abs("./gm_codes/")
	// return path + "/"
	return AppendWDPath("/gm_codes")
}

//getExecPath returns the executing path
func getExecPath() string {
	ex, err := os.Executable()
	if err != nil {
		panic(err)
	}
	exPath := filepath.Dir(ex)
	return exPath
}

//AppendWDPath append working dir path to the passed relative path
func AppendWDPath(path string) string {
	return getExecPath() + path
}
