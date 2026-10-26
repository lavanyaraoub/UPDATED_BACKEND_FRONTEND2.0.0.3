package helper

import (
	"bufio"
	"os"
	"strings"
)

//ReadSerialNumber read the unique serial number of the device
func ReadSerialNumber() (string, error) {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		currLine := scanner.Text()
		if strings.HasPrefix(currLine, "Serial") {
			splitted := strings.Split(currLine, ":")
			return strings.TrimSpace(splitted[1]), nil
		}
	}

	return "", nil
}
