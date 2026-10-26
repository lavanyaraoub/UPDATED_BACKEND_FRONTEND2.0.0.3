package configparser

import (
	dt "EtherCAT/datatypes"
	"EtherCAT/helper"
	logger "EtherCAT/logger"
	"io"
	"os"

	"gopkg.in/yaml.v2"
)

// ParseExececutionConfigYML parse execution yml file.
// This is the file-reading wrapper around ParseExecutionConfigFromReader.
// Use ParseExecutionConfigFromReader directly in tests.
//
// Note: function name preserves the original typo (Exececution) to avoid
// breaking existing callers. New code should use ParseExecutionConfigFromReader.
func ParseExececutionConfigYML() (dt.YamlConfig, error) {
	f, err := os.Open(helper.AppendWDPath("/configs/execution.yml"))
	if err != nil {
		logger.Error("Error reading execution config yaml file: ", err)
		return dt.YamlConfig{}, err
	}
	defer f.Close()
	return ParseExecutionConfigFromReader(f)
}

// ParseExecutionConfigFromReader parses an execution configuration from
// an io.Reader. Separated from ParseExececutionConfigYML for testability.
// Returns the parsed YamlConfig and any YAML unmarshal error.
func ParseExecutionConfigFromReader(r io.Reader) (dt.YamlConfig, error) {
	var yamlConfig dt.YamlConfig
	data, err := io.ReadAll(r)
	if err != nil {
		return yamlConfig, err
	}
	err = yaml.Unmarshal(data, &yamlConfig)
	return yamlConfig, err
}
