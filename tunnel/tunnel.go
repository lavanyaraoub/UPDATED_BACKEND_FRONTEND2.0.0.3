package tunnel

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"EtherCAT/constants"
	"EtherCAT/helper"
	"EtherCAT/logger"
	"EtherCAT/settings"
)

// execCommand is exec.Command by default. Tests override this to avoid
// ever invoking the real ngrok tunnel.sh script.
var execCommand = exec.Command

// ngrokLogPath is the real, hardcoded ngrok log location in production.
// Tests override this to point at a temp file instead.
var ngrokLogPath = "/mnt/app/jamun/ngrok.log"

type Tunnel struct{}

func NewTunnel() Tunnel {
	return Tunnel{}
}

/*
StartHTTP starts an ngrok HTTP tunnel and returns the public HTTPS URL.
*/
func (t *Tunnel) StartHTTP() (string, error) {
	logger.Debug("remote HTTP tunnel starting...")

	_, err := t.changeTunnelState("STARTHTTP")
	if err != nil {
		return "", err
	}

	var tunnelURL string

	// ngrok on Raspberry Pi is slow — wait up to 60s
	for i := 0; i < 60; i++ {
		tunnelURL, err = t.getHTTPTunnelURL()

		// fatal ngrok error
		if err != nil {
			return "", err
		}

		// success
		if tunnelURL != "" {
			return tunnelURL, nil
		}

		time.Sleep(1 * time.Second)
	}

	return "", errors.New("timeout waiting for HTTP tunnel URL")
}

/*
Stop terminates ngrok
*/
func (t *Tunnel) Stop() {
	_, _ = t.changeTunnelState("KILL")
}

/*
changeTunnelState invokes scripts/tunnel.sh
*/
func (t *Tunnel) changeTunnelState(command string) (string, error) {
	script := helper.AppendWDPath("/scripts/tunnel.sh")

	if err := os.Chmod(script, 0777); err != nil {
		return "", err
	}

	env := settings.GetEnvSettings()

	cmd := execCommand(
		script,
		"-a", command,
		"-t", constants.NgrokAuthToken,
		"-p", env.TunnelAccess,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", errors.New(err.Error() + " | " + stderr.String())
	}

	return stdout.String(), nil
}

/*
getHTTPTunnelURL parses /mnt/app/jamun/ngrok.log
and extracts the HTTPS ngrok URL
*/
func (t *Tunnel) getHTTPTunnelURL() (string, error) {
	file, err := os.Open(ngrokLogPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()

		// SUCCESS: HTTPS tunnel URL
		if strings.Contains(line, "url=https://") {
			idx := strings.Index(line, "url=")
			if idx >= 0 {
				return strings.TrimSpace(line[idx+4:]), nil
			}
		}

		// FAILURE: real ngrok error (ignore err=nil)
		if strings.Contains(line, "err=") {
			idx := strings.Index(line, "err=")
			if idx >= 0 {
				errVal := strings.TrimSpace(line[idx+4:])
				if errVal != "" && errVal != "nil" {
					return "", errors.New(errVal)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	// not ready yet (this is normal)
	return "", nil
}
