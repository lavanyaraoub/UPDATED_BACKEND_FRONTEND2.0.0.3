package systemupdate

import (
	"EtherCAT/channels"
	"EtherCAT/logger"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const rollbackScriptPath = "/mnt/app/jamun/scripts/rollback.sh"

var isRollbackInProgress bool

type BackupInfo struct {
	Path      string `json:"path"`
	Timestamp string `json:"timestamp"`
	Version   string `json:"version"`
}

func GetLatestBackup() (BackupInfo, error) {
	var info BackupInfo

	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return info, fmt.Errorf("cannot read backup root: %w", err)
	}

	type candidate struct {
		name    string
		created string
		version string
	}

	var candidates []candidate

	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".partial") {
			continue
		}

		meta, err := os.ReadFile(filepath.Join(backupRoot, e.Name(), "backup.meta"))
		if err != nil {
			continue
		}

		values := map[string]string{}
		for _, line := range strings.Split(string(meta), "\n") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				values[parts[0]] = parts[1]
			}
		}

		if values["STATUS"] != "verified" {
			continue
		}

		if _, err := os.Stat(filepath.Join(backupRoot, e.Name(), "manifest.tsv")); err != nil {
			continue
		}

		candidates = append(candidates, candidate{
			name:    e.Name(),
			created: values["CREATED_AT"],
			version: values["SOURCE_VERSION"],
		})
	}

	if len(candidates) == 0 {
		return info, fmt.Errorf("no backups found in %s", backupRoot)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].created < candidates[j].created
	})

	latest := candidates[len(candidates)-1]

	info.Path = filepath.Join(backupRoot, latest.name)
	info.Timestamp = latest.created
	info.Version = latest.version

	if info.Version == "" {
		info.Version = "unknown"
	}

	return info, nil
}

func PerformRollback(sendUINotification bool) error {
	if isRollbackInProgress {
		logger.Info("Rollback already in progress, ignoring request")
		return fmt.Errorf("rollback already in progress")
	}

	if isAnUpdateInProgress() {
		logger.Info("System update in progress, cannot rollback now")
		return fmt.Errorf("update in progress, cannot rollback")
	}

	backup, err := GetLatestBackup()
	if err != nil {
		logger.Error("Rollback aborted: ", err)
		if sendUINotification {
			channels.SendAlarm("Rollback failed: no backup available.")
		}
		return err
	}

	if _, err := os.Stat(rollbackScriptPath); os.IsNotExist(err) {
		logger.Error("Rollback script missing: ", rollbackScriptPath)
		if sendUINotification {
			channels.SendAlarm("Rollback failed: rollback script missing.")
		}
		return err
	}

	os.Chmod(rollbackScriptPath, 0750)

	isRollbackInProgress = true
	writeOTAStatus("ROLLBACK_REQUESTED", 5, "Rollback requested. Keep power ON and do not reboot.")

	logger.Info("Starting rollback to backup: ", backup.Path)

	cmd := exec.Command("bash", rollbackScriptPath, backup.Path, installPath)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		logger.Error("Failed to start rollback script: ", err)
		if sendUINotification {
			channels.SendAlarm("Rollback failed: unable to launch rollback script.")
		}
		isRollbackInProgress = false
		return err
	}

	logger.Info("Rollback script started. PID: ", cmd.Process.Pid)

	if sendUINotification {
		channels.SendSystemNotice("Restoring backup version " + backup.Version + ". Keep power ON and do not reboot until recovery completes.")
	}

	time.Sleep(3 * time.Second)

	isRollbackInProgress = false

	logger.Info("Exiting jamun so systemd can restart with rolled-back version...")
	os.Exit(2)

	return nil
}

func CheckRollbackSuccess() {
	data, err := os.ReadFile(filepath.Join(rtcUpdateRoot, "status"))
	if err != nil {
		return
	}

	values := map[string]string{}

	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}

	time.Sleep(5 * time.Second)

	switch values["state"] {
	case "SUCCESS":
		channels.SendSystemNotice(values["message"])
	case "ROLLBACK_COMPLETE":
		channels.SendSystemNotice(values["message"])
	}
}