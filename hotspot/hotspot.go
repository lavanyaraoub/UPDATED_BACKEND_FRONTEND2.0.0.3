package hotspot

import (
	"EtherCAT/helper"
	"bytes"
	"errors"
	"os/exec"
)

// execCommand is exec.Command by default. Tests override this to avoid
// ever invoking the real sudo hotspot.sh script.
var execCommand = exec.Command

type Hotspot struct {
	hotspotEnabled bool
}

func NewHotspot() Hotspot {
	return Hotspot{}
}

func (h *Hotspot) Create() {
	h.changeHotspotState("CREATE")
}

func (h *Hotspot) Kill() {
	h.changeHotspotState("KILL")
}

// changeHotspotState create or kill hotspot based on the gpio pin state.
//
//	command can be either CREATE or KILL
func (h *Hotspot) changeHotspotState(command string) (string, error) {
	c := execCommand("sudo", helper.AppendWDPath("/scripts/hotspot.sh"), "-a", command)
	stderr := &bytes.Buffer{}
	stdout := &bytes.Buffer{}
	c.Stderr = stderr
	c.Stdout = stdout
	if err := c.Run(); err != nil {
		return "", errors.New("Error: " + err.Error() + "|" + stderr.String())
	} else {
		return stdout.String(), nil
	}
}
