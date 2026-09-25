package configparser

import (
	dt "EtherCAT/ethercatdevicedatatypes"
	"EtherCAT/helper"
	logger "EtherCAT/logger"
	"io"
	"os"

	"gopkg.in/yaml.v2"
)

// ParseEthercatAddressConfig parse ethercat-device-addressing yml file
// (e.g., a6minas.yml, M700.yml). Takes a filename relative to the
// executable working directory.
// This is the file-reading wrapper around ParseEthercatAddressConfigFromReader.
// Use ParseEthercatAddressConfigFromReader directly in tests.
// https://stackoverflow.com/a/39832919
func ParseEthercatAddressConfig(fileName string) (dt.Ethercat, error) {
	f, err := os.Open(helper.AppendWDPath(fileName))
	if err != nil {
		logger.Error("Error reading ethercat-device-addressing yaml file: ", err)
		return dt.Ethercat{}, err
	}
	defer f.Close()
	return ParseEthercatAddressConfigFromReader(f)
}

// ParseEthercatAddressConfigFromReader parses an ethercat device addressing
// configuration from an io.Reader. Separated from ParseEthercatAddressConfig
// for testability — tests can pass a strings.NewReader containing test YAML
// without needing real files.
// Returns the parsed Ethercat and any YAML unmarshal error.
func ParseEthercatAddressConfigFromReader(r io.Reader) (dt.Ethercat, error) {
	var ethercatOperation dt.Ethercat
	data, err := io.ReadAll(r)
	if err != nil {
		return ethercatOperation, err
	}
	err = yaml.Unmarshal(data, &ethercatOperation)
	return ethercatOperation, err
}
