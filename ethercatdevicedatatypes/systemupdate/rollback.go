package systemupdate

import (
	"EtherCAT/channels"
	"EtherCAT/logger"
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	rollbackScriptPath = "/usr/local/lib/jamun-ota/rollback.sh"
	slotStatePath      = "/var/lib/jamun-ota/state"
)

var isRollbackInProgress bool

type BackupInfo struct {
	Path      string `json:"path"`
	Timestamp string `json:"timestamp"`
	Version   string `json:"version"`
}

func readSlotState() map[string]string {
	values := map[string]string{}
	f, err := os.Open(slotStatePath)
	if err != nil { return values }
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.SplitN(s.Text(), "=", 2)
		if len(parts) == 2 { values[parts[0]] = parts[1] }
	}
	return values
}

// GetLatestBackup preserves the existing API shape, but now returns the
// previous verified A/B slot. Rollback never copies application files.
func GetLatestBackup() (BackupInfo, error) {
	state := readSlotState()
	slot := state["PREVIOUS_SLOT"]
	if slot != "A" && slot != "B" { return BackupInfo{}, fmt.Errorf("no previous verified slot") }
	path := filepath.Join(releasesRoot, "slot-"+slot)
	versionBytes, err := os.ReadFile(filepath.Join(path, "version.txt"))
	if err != nil { return BackupInfo{}, fmt.Errorf("cannot read rollback slot version: %w", err) }
	info, err := os.Stat(path)
	if err != nil { return BackupInfo{}, err }
	return BackupInfo{Path:path, Timestamp:info.ModTime().Format(time.RFC3339), Version:strings.TrimSpace(string(versionBytes))}, nil
}

func PerformRollback(sendUI bool) error {
	if isRollbackInProgress { return fmt.Errorf("rollback already in progress") }
	if isAnUpdateInProgress() { return fmt.Errorf("update in progress, cannot rollback") }
	target, err := GetLatestBackup()
	if err != nil { return err }
	if _, err := os.Stat(rollbackScriptPath); err != nil { return err }
	isRollbackInProgress = true
	defer func(){ isRollbackInProgress = false }()
	cmd := exec.Command("bash", rollbackScriptPath, filepath.Base(target.Path))
	if err := cmd.Start(); err != nil { return err }
	logger.Info("Rollback handoff started for ", target.Path)
	if sendUI { channels.SendAlarm("Rollback started. Switching to verified version " + target.Version + ".") }
	time.Sleep(3*time.Second)
	os.Exit(2)
	return nil
}

// CheckRollbackSuccess commits a candidate only after Jamun initialized and
// stayed alive for the stabilization interval.
func CheckRollbackSuccess() {
	time.Sleep(15*time.Second)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:5000", 3*time.Second)
	if err != nil {
		logger.Error("Cannot commit A/B boot: REST health port 5000 is unavailable: ", err)
		return
	}
	_ = conn.Close()
	cmd := exec.Command("/usr/local/lib/jamun-ota/mark_boot_success.sh")
	if output, err := cmd.CombinedOutput(); err != nil {
		logger.Error("Cannot commit A/B boot: ", err, " output=", string(output))
		return
	}
	state := readSlotState()
	if state["LAST_EVENT"] == "rollback" {
		channels.SendAlarm("Rollback complete. Now running version " + readInstalledVersion() + ".")
	}
}
