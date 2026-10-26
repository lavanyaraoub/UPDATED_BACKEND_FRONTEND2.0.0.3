package systemupdate

import (
	"EtherCAT/channels"
	"EtherCAT/logger"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var checkForUpdateInProgress bool

type releaseInfo struct {
	Filename       string
	ObjectName     string
	DownloadURL    string
	ChecksumURL    string
	ExpectedSize   int64
	ExpectedSHA256 string
}

func CheckforUpdates(sendUI bool) {
	if checkForUpdateInProgress || isAnUpdateInProgress() {
		logger.Info("An update operation is already in progress")
		return
	}
	checkForUpdateInProgress = true
	defer func() { checkForUpdateInProgress = false }()

	if err := fetchRelease(sendUI); err != nil {
		logger.Error("OTA download/staging failed: ", err)
		if cleanupErr := resetTransientOTAWorkspace(); cleanupErr != nil {
			logger.Error("OTA workspace cleanup failed: ", cleanupErr)
		}
		if sendUI {
			channels.SendAlarm("Update download failed: " + err.Error())
		}
	}
}

// resetTransientOTAWorkspace removes only retryable OTA data. The installed
// application, verified backups and unified logs are outside rtcUpdateRoot.
// downloads and staging are recreated empty for the next update check.
func resetTransientOTAWorkspace() error {
	if rtcUpdateRoot != "/home/pi/rtc_updates" {
		return fmt.Errorf("refusing cleanup for unexpected OTA root %q", rtcUpdateRoot)
	}
	entries, err := os.ReadDir(rtcUpdateRoot)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(rtcUpdateRoot, entry.Name())); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(rtcUpdateRoot, "downloads"), 0750); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(rtcUpdateRoot, "staging"), 0750)
}

func fetchRelease(sendUI bool) error {
	release, err := getLatestReleaseInfo()
	if err != nil {
		return fmt.Errorf("release discovery failed: %w", err)
	}
	incoming := extractVersion(release.Filename)
	installed := readInstalledVersion()
	if installed != "" && compareVersions(incoming, installed) <= 0 {
		if sendUI {
			channels.SendAlarm("No update available. Installed version " + installed + " is current.")
		}
		return nil
	}

	if release.ExpectedSHA256 == "" {
		release.ExpectedSHA256, err = fetchChecksum(release.ChecksumURL, release.Filename)
		if err != nil {
			return fmt.Errorf("published SHA-256 unavailable: %w", err)
		}
	}

	downloadDir := filepath.Join(rtcUpdateRoot, "downloads")
	stagingDir := filepath.Join(rtcUpdateRoot, "staging")
	stageFinal := filepath.Join(stagingDir, strings.TrimSuffix(release.Filename, ".tar.gz"))
	tarFinal := filepath.Join(downloadDir, release.Filename)
	metaPath := filepath.Join(rtcUpdateRoot, "current_update")
	if err := os.MkdirAll(downloadDir, 0750); err != nil { return err }
	if err := os.MkdirAll(stagingDir, 0750); err != nil { return err }
	if err := checkDiskSpace(rtcUpdateRoot, uint64(release.ExpectedSize)*3+100*1024*1024); err != nil {
		return fmt.Errorf("insufficient disk space: %w", err)
	}

	_ = os.Remove(metaPath)
	var lastErr error
	for attempt, delay := range []time.Duration{0, 10 * time.Second, 30 * time.Second} {
		if attempt > 0 {
			if sendUI { channels.SendAlarm(fmt.Sprintf("Download interrupted. Retrying (%d/3)", attempt+1)) }
			time.Sleep(delay)
		}
		_ = os.Remove(tarFinal + ".part")
		_ = os.RemoveAll(stageFinal + ".partial")
		lastErr = downloadAndVerify(release, tarFinal)
		if lastErr == nil { break }
		logger.Error("OTA download attempt failed: ", lastErr)
	}
	if lastErr != nil { return lastErr }

	stagePartial := stageFinal + ".partial"
	_ = os.RemoveAll(stagePartial)
	if err := os.MkdirAll(stagePartial, 0750); err != nil { return err }
	f, err := os.Open(tarFinal)
	if err != nil { return err }
	err = Untar(f, stagePartial)
	_ = f.Close()
	if err != nil { _ = os.RemoveAll(stagePartial); return fmt.Errorf("archive extraction failed: %w", err) }

	// The package contains one top-level jamun_vX directory.
	extracted := filepath.Join(stagePartial, strings.TrimSuffix(release.Filename, ".tar.gz"))
	if err := validateStagedUpdate(extracted); err != nil {
		_ = os.RemoveAll(stagePartial)
		return err
	}
	_ = os.RemoveAll(stageFinal)
	if err := os.Rename(extracted, stageFinal); err != nil { return err }
	_ = os.RemoveAll(stagePartial)

	// Seal the extracted tree independently from the archive checksum.  This
	// manifest is stored outside stageFinal so it cannot include/checksum
	// itself. pre_start_ota.sh revalidates both the archive and this seal
	// immediately before touching the live installation.
	stageManifest := stageFinal + ".manifest.tsv"
	if err := writeTreeSHA256Manifest(stageFinal, stageManifest); err != nil {
		_ = os.RemoveAll(stageFinal)
		_ = os.Remove(stageManifest)
		return fmt.Errorf("cannot seal staged update: %w", err)
	}
	stageManifestSHA, _, err := sha256File(stageManifest)
	if err != nil { return fmt.Errorf("cannot checksum staging manifest: %w", err) }

	meta := fmt.Sprintf("Name: %s\nVersion: %s\nSize: %d\nSHA256: %s\nArchivePath: %s\nStagingManifest: %s\nStagingSHA256: %s\nState: ready\nBackup:\nCreatedAt: %s\n",
		release.Filename, incoming, release.ExpectedSize, release.ExpectedSHA256,
		tarFinal, stageManifest, stageManifestSHA, time.Now().Format(time.RFC3339))
	if err := atomicWriteFile(metaPath, []byte(meta), 0640); err != nil { return err }
	if sendUI { channels.SendAlarm("Update " + incoming + " downloaded and SHA-256 verified. Click Update to install.") }
	return nil
}

// writeTreeSHA256Manifest writes a deterministic, filename-safe manifest:
// SHA256<TAB>SIZE<TAB>relative/path. Newlines and tabs in package names are
// rejected so the shell verifier can parse the same format unambiguously.
func writeTreeSHA256Manifest(root, manifestPath string) error {
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil { return err }
		if !info.Mode().IsRegular() { return nil }
		rel, err := filepath.Rel(root, path)
		if err != nil { return err }
		if strings.ContainsAny(rel, "\t\n\r") { return fmt.Errorf("unsupported staged filename %q", rel) }
		paths = append(paths, rel)
		return nil
	})
	if err != nil { return err }
	if len(paths) == 0 { return fmt.Errorf("staged tree contains no files") }
	sort.Strings(paths)
	var b strings.Builder
	for _, rel := range paths {
		sha, size, err := sha256File(filepath.Join(root, rel))
		if err != nil { return err }
		fmt.Fprintf(&b, "%s\t%d\t%s\n", sha, size, filepath.ToSlash(rel))
	}
	return atomicWriteFile(manifestPath, []byte(b.String()), 0640)
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil { return "", 0, err }
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil { return "", 0, err }
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func downloadAndVerify(release releaseInfo, finalPath string) error {
	resp, err := http.Get(release.DownloadURL)
	if err != nil { return err }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { return fmt.Errorf("download returned %s", resp.Status) }
	part := finalPath + ".part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil { return err }
	h := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, release.ExpectedSize+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(part)
		if copyErr != nil { return copyErr }; if syncErr != nil { return syncErr }; return closeErr
	}
	if written != release.ExpectedSize {
		_ = os.Remove(part)
		return fmt.Errorf("SIZE_MISMATCH: expected %d bytes, received %d", release.ExpectedSize, written)
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, release.ExpectedSHA256) {
		_ = os.Remove(part)
		return fmt.Errorf("SHA256_MISMATCH: update integrity check failed")
	}
	_ = os.Remove(finalPath)
	return os.Rename(part, finalPath)
}

func getLatestReleaseInfo() (releaseInfo, error) {
	const listURL = "https://storage.googleapis.com/storage/v1/b/jamun-ota-releases/o?prefix=releases/"
	resp, err := http.Get(listURL)
	if err != nil { return releaseInfo{}, err }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { return releaseInfo{}, fmt.Errorf("bucket list returned %s", resp.Status) }
	var result struct { Items []struct { Name, Size string; Metadata map[string]string `json:"metadata"` } `json:"items"` }
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil { return releaseInfo{}, err }
	var candidates []releaseInfo
	for _, item := range result.Items {
		if !strings.HasSuffix(item.Name, ".tar.gz") { continue }
		size, err := strconv.ParseInt(item.Size, 10, 64)
		if err != nil || size <= 0 { continue }
		name := filepath.Base(item.Name)
		candidates = append(candidates, releaseInfo{Filename:name, ObjectName:item.Name, ExpectedSize:size,
			ExpectedSHA256:strings.TrimSpace(item.Metadata["sha256"]),
			DownloadURL:"https://storage.googleapis.com/jamun-ota-releases/"+item.Name,
			ChecksumURL:"https://storage.googleapis.com/jamun-ota-releases/"+item.Name+".sha256"})
	}
	if len(candidates) == 0 { return releaseInfo{}, fmt.Errorf("no valid .tar.gz release found") }
	sort.Slice(candidates, func(i, j int) bool {
		return compareVersions(extractVersion(candidates[i].Filename), extractVersion(candidates[j].Filename)) < 0
	})
	latest := candidates[len(candidates)-1]
	if len(latest.ExpectedSHA256) != 64 {
		latest.ExpectedSHA256 = ""
	}
	return latest, nil
}

func fetchChecksum(url, filename string) (string, error) {
	resp, err := http.Get(url); if err != nil { return "", err }; defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { return "", fmt.Errorf("checksum returned %s", resp.Status) }
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096)); if err != nil { return "", err }
	fields := strings.Fields(string(b)); if len(fields) == 0 || len(fields[0]) != 64 { return "", fmt.Errorf("invalid checksum for %s", filename) }
	if _, err := hex.DecodeString(fields[0]); err != nil { return "", fmt.Errorf("invalid checksum: %w", err) }
	return strings.ToLower(fields[0]), nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode); if err != nil { return err }
	if _, err = f.Write(data); err == nil { err = f.Sync() }
	if closeErr := f.Close(); err == nil { err = closeErr }
	if err != nil { _ = os.Remove(tmp); return err }
	return os.Rename(tmp, path)
}

func readInstalledVersion() string { b, err := os.ReadFile(filepath.Join(installPath,"version.txt")); if err != nil{return ""}; return strings.TrimSpace(string(b)) }
func extractVersion(filename string) string { return strings.TrimPrefix(strings.TrimSuffix(filename,".tar.gz"),"jamun_v") }

func compareVersions(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	length := len(ap)
	if len(bp) > length { length = len(bp) }
	for i := 0; i < length; i++ {
		av, bv := 0, 0
		if i < len(ap) { av, _ = strconv.Atoi(ap[i]) }
		if i < len(bp) { bv, _ = strconv.Atoi(bp[i]) }
		if av < bv { return -1 }
		if av > bv { return 1 }
	}
	return 0
}

func checkDiskSpace(path string, neededBytes uint64) error {
	var stat syscallStatfs
	if err := syscallStatfsCall(path, &stat); err != nil { return nil }
	available := stat.Bavail*uint64(stat.Bsize)
	if available < neededBytes { return fmt.Errorf("%d MB available, need %d MB",available/1024/1024,neededBytes/1024/1024) }
	return nil
}

func validateStagedUpdate(stage string) error {
	for _, name := range []string{"jamun","libethercatinterface.so","ethercatinterface.h","version.txt"} {
		i, err := os.Stat(filepath.Join(stage,name)); if err != nil || i.IsDir() || i.Size()==0 { return fmt.Errorf("required staged file invalid: %s",name) }
	}
	for _, name := range []string{"commands","configs","scripts","www_v2"} {
		i, err := os.Stat(filepath.Join(stage,name)); if err != nil || !i.IsDir() { return fmt.Errorf("required staged directory invalid: %s",name) }
	}
	return nil
}
