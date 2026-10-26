package configparser

import (
	"EtherCAT/helper"
	"EtherCAT/logger"
	"bufio"
	"os"
	"strings"
	"sync"
)

var (
	errFileMu     sync.Mutex
	parsedErrFile bool
	errKeyValue   map[string]string
	// errDefinitionPath is the path to the error definition file.
	// Override in tests to inject a custom file without touching the filesystem.
	errDefinitionPath = ""
)

// GetErrorString returns the description for the given error code id.
// Thread-safe: protected by errFileMu so concurrent callers during startup
// do not race on parsedErrFile or errKeyValue.
func GetErrorString(id string) string {
	errFileMu.Lock()
	if !parsedErrFile {
		err := parseErrFile()
		if err != nil {
			logger.Error(err)
		}
	}
	errFileMu.Unlock()

	if _, ok := errKeyValue[id]; !ok {
		return "Unknown error code " + id
	}
	return errKeyValue[id]
}

func parseErrFile() error {
	errKeyValue = make(map[string]string)
	path := errDefinitionPath
	if path == "" {
		path = helper.AppendWDPath("/configs/error_definition.txt")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		line := scanner.Text()
		// Skip comment lines (starting with /)
		if strings.HasPrefix(line, "/") {
			continue
		}
		// Skip blank lines
		if strings.TrimSpace(line) == "" {
			continue
		}
		splitted := strings.SplitN(line, ":", 2)
		// Guard against malformed lines with no colon separator.
		// Without this check, splitted[1] panics with index out of range.
		if len(splitted) < 2 {
			logger.Error("err_definition_parser: malformed line (no colon separator):", line)
			continue
		}
		errKeyValue[strings.TrimSpace(splitted[0])] = strings.TrimSpace(splitted[1])
	}
	parsedErrFile = true
	return nil
}
