package rollingfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Common constants
const (
	rollingLogHistoryDelimiter = "."
)

// Types of the rolling writer: roll by date, by time, etc.
type rollingType uint8

const (
	rollingTypeSize = iota
	rollingTypeTime
)


// rollerVirtual is an interface that represents all virtual funcs that are
// called in different rolling writer subtypes.
type rollerVirtual interface {
	needsToRoll() bool                                  // Returns true if needs to switch to another file.
	isFileRollNameValid(rname string) bool              // Returns true if logger roll file name (postfix/prefix/etc.) is ok.
	sortFileRollNamesAsc(fs []string) ([]string, error) // Sorts logger roll file names in ascending order of their creation by logger.

	// getNewHistoryRollFileName is called whenever we are about to roll the
	// current log file. It returns the name the current log file should be
	// rolled to.
	getNewHistoryRollFileName(otherHistoryFiles []string) string

	getCurrentFileName() string
}

type rollingFile struct {
	fileName        string // log file name
	currentDirPath  string
	currentFile     *os.File
	currentName     string
	currentFileSize int64
	rollingType     rollingType // Rolling mode (Files roll by size/date/...)
	maxRolls        int

	self            rollerVirtual // Used for virtual calls

	rollLock        sync.Mutex
}

func newRollingFile(fpath string, rtype rollingType, maxr int) (*rollingFile, error) {
	rf := new(rollingFile)
	rf.currentDirPath, rf.fileName = filepath.Split(fpath)
	if len(rf.currentDirPath) == 0{
		rf.currentDirPath = "."
	}

	rf.rollingType = rtype
	rf.maxRolls = maxr
	return rf, nil
}


func(rf *rollingFile) roll() error {
	// First, close current file.
	err := rf.currentFile.Close()
	if err != nil {
		return err
	}
	rf.currentFile = nil

	// Current history of all previous log files.
	// For file roller it may be like this:
	//     * ...
	//     * file.log.4
	//     * file.log.5
	//     * file.log.6
	//
	// For date roller it may look like this:
	//     * ...
	//     * file.log.11.Aug.13
	//     * file.log.15.Aug.13
	//     * file.log.16.Aug.13
	// Sorted log history does NOT include current file.
	history, err := rf.getSortedLogHistory()
	if err != nil {
		return err
	}
	// Renames current file to create a new roll history entry
	// For file roller it may be like this:
	//     * ...
	//     * file.log.4
	//     * file.log.5
	//     * file.log.6
	//     n file.log.7  <---- RENAMED (from file.log)
	newHistoryName := rf.createFullFileName(rf.fileName,
		rf.self.getNewHistoryRollFileName(history))

	err = os.Rename(filepath.Join(rf.currentDirPath, rf.currentName), filepath.Join(rf.currentDirPath, newHistoryName))
	if err != nil {
		return err
	}

	// Finally, add the newly added history file to the history archive
	// and, if after that the archive exceeds the allowed max limit, older rolls
	// must the removed/archived.
	history = append(history, newHistoryName)
	if len(history) > rf.maxRolls {
		err = rf.deleteOldRolls(history)
		if err != nil {
			return err
		}
	}

	return nil
}


func (rf *rollingFile) hasRollName(file string) bool {
	rname := rf.fileName + rollingLogHistoryDelimiter
	return strings.HasPrefix(file, rname)
}


func (rf *rollingFile) getFileRollName(fileName string) string {
	return fileName[len(rf.fileName+rollingLogHistoryDelimiter):]
}


func (rf *rollingFile) createFullFileName(originalName, rollname string) string {
	return originalName + rollingLogHistoryDelimiter + rollname
}

func (rf *rollingFile) getSortedLogHistory() ([]string, error) {
	files, err := getDirFilePaths(rf.currentDirPath, nil, true)
	if err != nil {
		return nil, err
	}
	var validRollNames []string
	for _, file := range files {
		if rf.hasRollName(file) {
			rname := rf.getFileRollName(file)
			if rf.self.isFileRollNameValid(rname) {
				validRollNames = append(validRollNames, rname)
			}
		}
	}
	sortedTails, err := rf.self.sortFileRollNamesAsc(validRollNames)
	if err != nil {
		return nil, err
	}
	validSortedFiles := make([]string, len(sortedTails))
	for i, v := range sortedTails {
		validSortedFiles[i] = rf.createFullFileName(rf.fileName, v)
	}
	return validSortedFiles, nil
}



func (rf *rollingFile) deleteOldRolls(history []string) error {
	if rf.maxRolls <= 0 {
		return nil
	}

	rollsToDelete := len(history) - rf.maxRolls
	if rollsToDelete <= 0 {
		return nil
	}

	var err error
	// In all cases (archive files or not) the files should be deleted.
	for i := 0; i < rollsToDelete; i++ {
		// Try best to delete files without breaking the loop.
		if err = tryRemoveFile(filepath.Join(rf.currentDirPath, history[i])); err != nil {
			fmt.Fprintf(os.Stderr, "logrus rolling file hook internal error: %s\n", err)
		}
	}

	return nil
}



func (rf *rollingFile) createFileAndFolderIfNeeded() error {
	var err error

	if len(rf.currentDirPath) != 0 {
		err = os.MkdirAll(rf.currentDirPath, defaultDirectoryPermissions)

		if err != nil {
			return err
		}
	}
	rf.currentName = rf.self.getCurrentFileName()
	filePath := filepath.Join(rf.currentDirPath, rf.currentName)

	// This will either open the existing file (without truncating it) or
	// create if necessary. Append mode avoids any race conditions.
	rf.currentFile, err = os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, defaultFilePermissions)
	if err != nil {
		return err
	}

	stat, err := rf.currentFile.Stat()
	if err != nil {
		rf.currentFile.Close()
		rf.currentFile = nil
		return err
	}

	rf.currentFileSize = stat.Size()
	return nil
}


func (rf *rollingFile) Close() error {
	if rf.currentFile != nil {
		e := rf.currentFile.Close()
		if e != nil {
			return e
		}
		rf.currentFile = nil
	}
	return nil
}