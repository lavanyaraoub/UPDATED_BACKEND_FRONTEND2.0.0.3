package configparser

import (
	dt "EtherCAT/ethercatdevicedatatypes"
	"EtherCAT/helper"
	logger "EtherCAT/logger"
	"io"
	"os"

	"gopkg.in/yaml.v2"
)

// ParseDeviceConfig parse device-configuration.yml file.
// This is the file-reading wrapper around ParseDeviceConfigFromReader.
// Use ParseDeviceConfigFromReader directly in tests.
// https://stackoverflow.com/a/39832919
func ParseDeviceConfig() (dt.Devices, error) {
	f, err := os.Open(helper.AppendWDPath("/configs/device-configuration.yml"))
	if err != nil {
		logger.Error("Error reading device-configuration yaml file: ", err)
		return dt.Devices{}, err
	}
	defer f.Close()
	return ParseDeviceConfigFromReader(f)
}

// ParseDeviceConfigFromReader parses a device configuration from an io.Reader.
// Separated from ParseDeviceConfig for testability — tests can pass a
// strings.NewReader containing test YAML without needing real files.
// Returns the parsed Devices and any YAML unmarshal error.
func ParseDeviceConfigFromReader(r io.Reader) (dt.Devices, error) {
	var devices dt.Devices
	data, err := io.ReadAll(r)
	if err != nil {
		return devices, err
	}
	err = yaml.Unmarshal(data, &devices)
	return devices, err
}
