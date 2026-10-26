//go:build integration

package configparser

import (
	"strings"
	"testing"
)

const integrationExecutionYAML = `---
execution:
    command:
        - cmd: G90
          func: g90
          description: absolute mode
          considerInBlockExecution: 0
        - cmd: G91
          func: g91
          description: relative mode
          considerInBlockExecution: 0
        - cmd: A**
          func: moveRotaryDegree
          driveId: 0
          description: A axis rotary move
          considerInBlockExecution: 1
        - cmd: B**
          func: moveRotaryDegree
          driveId: 1
          description: B axis rotary move
          considerInBlockExecution: 1
        - cmd: M30
          func: m30
          description: end program
          considerInBlockExecution: 0
        - cmd: INVALID
          func: invalidCommand
          description: invalid command
          considerInBlockExecution: 0
`

const integrationDeviceYAML = `---
devices:
    - device:
      name: A
      vendor-id: 0x0000066f
      product-code: 0x60380008
      rpm-const: 120000
      drive-x-ratio: 20000
      alias: 0
      id: 0
      address-config-name: a6minas
      address-config-file: "/configs/a6minas.yml"
      pot-not-threshold: 1.5
      stop-when-hardware-potnot: true
      io-poll-interval: 10000
`

const integrationEthercatYAML = `---
ethercat:
    - operation:
      name: configure
      steps:
        - step:
          name: operation mode
          address: 0x6060
          subindex: 0x00
          value: "0x1"
          delay: 0
          isBinary: false
          dataType: U8
    - operation:
      name: moveAbsolute
      steps:
        - step:
          name: target position
          address: 0x607A
          subindex: 0x00
          value: "100000"
          delay: 0
          isBinary: false
          dataType: U32
`

func TestConfigToExecutionIntegration_CommandDeviceAndEthercatConfigsAgree(t *testing.T) {
	execCfg, err := ParseExecutionConfigFromReader(strings.NewReader(integrationExecutionYAML))
	if err != nil {
		t.Fatalf("ParseExecutionConfigFromReader failed: %v", err)
	}
	devices, err := ParseDeviceConfigFromReader(strings.NewReader(integrationDeviceYAML))
	if err != nil {
		t.Fatalf("ParseDeviceConfigFromReader failed: %v", err)
	}
	ethercatCfg, err := ParseEthercatAddressConfigFromReader(strings.NewReader(integrationEthercatYAML))
	if err != nil {
		t.Fatalf("ParseEthercatAddressConfigFromReader failed: %v", err)
	}

	moveA := execCfg.Execution.GetCommand("A90")
	if moveA.Func != "moveRotaryDegree" || moveA.DriveID != 0 || moveA.ConsiderInBlockExecution != 1 {
		t.Fatalf("A90 mapping = %#v, want moveRotaryDegree drive 0 blocking motion", moveA)
	}
	moveB := execCfg.Execution.GetCommand("B-10")
	if moveB.Func != "moveRotaryDegree" || moveB.DriveID != 1 || moveB.ConsiderInBlockExecution != 1 {
		t.Fatalf("B-10 mapping = %#v, want moveRotaryDegree drive 1 blocking motion", moveB)
	}
	if len(devices.Device) != 1 {
		t.Fatalf("len(devices) = %d, want 1", len(devices.Device))
	}
	if devices.Device[0].Name != "A" || devices.Device[0].AddressConfigName != "a6minas" {
		t.Fatalf("device config = %#v, want A bound to a6minas", devices.Device[0])
	}
	if _, err := ethercatCfg.GetOperation("configure"); err != nil {
		t.Fatalf("configure operation not found: %v", err)
	}
	if _, err := ethercatCfg.GetOperation("moveAbsolute"); err != nil {
		t.Fatalf("moveAbsolute operation not found: %v", err)
	}
}

func TestConfigToExecutionIntegration_InvalidCommandFallbackIsConfigured(t *testing.T) {
	execCfg, err := ParseExecutionConfigFromReader(strings.NewReader(integrationExecutionYAML))
	if err != nil {
		t.Fatalf("ParseExecutionConfigFromReader failed: %v", err)
	}
	invalid := execCfg.Execution.GetCommand("Z123")
	if invalid.Func != "invalidCommand" {
		t.Fatalf("unknown command mapped to %#v, want invalidCommand", invalid)
	}
}
