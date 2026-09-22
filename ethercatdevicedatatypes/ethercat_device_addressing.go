package ethercatdevicedatatypes

import (
	"errors"
	"strconv"
)

// Step defines one single address write to the device.
type Step struct {
	Name     string `yaml:"name"`
	Address  int    `yaml:"address"`
	SubIndex int    `yaml:"subindex"`
	Value    string `yaml:"value"`
	Delay    int    `yaml:"delay"`
	IsBinary bool   `yaml:"isBinary"`
	DataType string `yaml:"dataType"`
	Action   string `yaml:"action"`
}

// GetValue return the value as uint32. If the value is in binary format then
// convert the binary to uint32
func (s *Step) GetValue() (int64, error) {
	if s.IsBinary {
		i, err := strconv.ParseInt(s.Value, 2, 64)
		return i, err
	}
	i, err := strconv.ParseInt(s.Value, 0, 64)
	return i, err
}

// GetValue16 return the value as uint16. If the value is in binary format then
// convert the binary to uint16
func (s *Step) GetValue16() (uint16, error) {
	if s.IsBinary {
		i, err := strconv.ParseInt(s.Value, 2, 32)
		return uint16(i), err
	}
	i, err := strconv.ParseInt(s.Value, 0, 32)
	return uint16(i), err
}

// Operation type holds the operation and steps involved
type Operation struct {
	Name  string `yaml:"name"`
	Steps []Step `yaml:"steps"`
}

// Ethercat type holds all different operations
type Ethercat struct {
	Operation []Operation `yaml:"ethercat"`
}

// GetOperation returns the operation based on the passed operation name
func (e *Ethercat) GetOperation(name string) (Operation, error) {
	for _, item := range e.Operation {
		if item.Name == name {
			return item, nil
		}
	}
	err := errors.New("Unable to find the given operation")
	var emptyOperation Operation
	return emptyOperation, err
}
