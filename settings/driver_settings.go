package settings

import (
	"EtherCAT/channels"
	"EtherCAT/helper"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	"sync"
)

// DriverSettings keeps the settings of the driver
// If the settings file uses values are enclosed in brackets use this approach
// https://stackoverflow.com/a/9573928/955092
// eg. FinishSignal       int       `json:"fin_signal,int"`
//
//	HomingOffset       float32   `json:"homing_offset,float32"`
type DriverSettings struct {
	FinishSignal       int          `json:"fin_signal"`
	WorkOffSet         float64      `json:"work_offset,string"`
	JogFeed            int          `json:"jog_feed,string"`
	HomingOffset       float32      `json:"homing_offset,string"`
	HomeDirection      int          `json:"home_dir"`
	GearRation         string       `json:"gear_ratio"`
	ECSFinTiming       int          `json:"timing,string"`
	BackLash           float64      `json:"back_lash,string"`
	ECS                int          `json:"ecs"`
	ClampDeclamp       int          `json:"cl_dl"`
	G55                float64      `json:"g55,string"`
	G54                float64      `json:"g54,string"`
	NOT                int          `json:"not,string"`
	G57                float64      `json:"g57,string"`
	MotorDirection     int          `json:"motor_dir"`
	G58                float64      `json:"g58,string"`
	ClampDeclampTiming int          `json:"cldl_timing,string"`
	POT                int          `json:"pot,string"`
	G56                float64      `json:"g56,string"`
	PitchError         []Float64Str `json:"pitch_error"`
	Mode               string
	FactorBacklash     int
	BinaryPosFeeds     []BinaryPosFeed `json:"binary_pos_feed"`
	LineNumber         string          `json:"line_number,omitempty"`
	// EncoderZeroApos is the raw encoder count captured only after a successful
	// multiturn reset. HomingApos is the fixed raw count representing machine
	// 0 degrees after applying the machine-specific HomingOffset.
	EncoderZeroApos   int32 `json:"encoder_zero_apos,omitempty"`
	HomingApos        int32 `json:"homing_apos,omitempty"`
	HomingDriveXRatio int   `json:"homing_drive_x_ratio,omitempty"`
	// A separate validity flag is required because encoder count 0 is a valid
	// home position immediately after a multiturn reset.
	HomingValid bool `json:"homing_valid,omitempty"`
}

type BinaryPosFeed struct {
	Binary    string   `json:"binary"`
	Position  string   `json:"pos"`
	Direction int16    `json:"dir"`
	FeedRate  Int32Str `json:"feed_rate"`
}

// Int32Str mirrors Float64Str's exact lenient-parsing pattern (below) for
// int32 fields. Added to fix a real bug: BinaryPosFeed.FeedRate was a plain
// int32 (no ,string tag), but settings.json has been written inconsistently
// over time — some feed_rate entries are bare numbers ("feed_rate": 20),
// others are quoted strings ("feed_rate": "8"). The quoted entries failed
// to unmarshal into a plain int32.
//
// CRITICAL DOWNSTREAM EFFECT THIS CAUSED: LoadDriverSettings treats ANY
// json.Unmarshal error as fully fatal — see the comment on LoadDriverSettings
// itself. Even though Go's decoder actually populates every OTHER field
// correctly (verified directly: JogFeed, POT, PitchError, etc. all decode
// fine even with this one field's type mismatch elsewhere in the same
// object), the production code discarded the ENTIRE parsed settings object
// because it only checked "did Unmarshal return an error at all" — not
// "did decoding actually fail for the fields I need." This meant every
// driver setting for the affected drive silently read as its zero value:
// JogFeed=0 (jog appeared completely dead), POT=0/NOT=0, and an empty
// PitchError slice (the exact crash found earlier in this project,
// motordriver.getPitchError indexing into a zero-length slice).
type Int32Str int32

func (i Int32Str) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.Itoa(int(i)))
}

func (i *Int32Str) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		value, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return err
		}
		*i = Int32Str(value)
		return nil
	}
	return json.Unmarshal(b, (*int32)(i))
}

// Float64Str custom parsing for PitchError, pitch error is coming from client as float string
// this change is based on the post from https://stackoverflow.com/questions/49415573/golang-json-how-do-i-unmarshal-array-of-strings-into-int64
type Float64Str float64

type TextProgramConfig struct {
	IP                      string `json:"ip"`
	User                    string `json:"user"`
	Password                string `json:"password"`
	Path                    string `json:"path"`
	JogClockwisePath        string `json:"jogClockwisePath"`
	JogCounterClockwisePath string `json:"jogCounterClockwisePath"`
}

// SaveTextProgramConfig marshals the struct to JSON and saves it to a file
func SaveTextProgramConfig(config TextProgramConfig) error {
	// Convert the struct into a nicely formatted JSON byte slice
	fileData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err // Return error if converting to JSON fails
	}

	// Write the JSON data to the textprogram.json file
	return os.WriteFile("/mnt/app/jamun/settings/textprogram.json", fileData, 0644)
}

// for renishaw to load the text
func LoadTextProgramConfig() (TextProgramConfig, error) {
	var config TextProgramConfig

	fileData, err := os.ReadFile("/mnt/app/jamun/settings/textprogram.json")
	if err != nil {
		return config, err
	}

	err = json.Unmarshal(fileData, &config)
	return config, err
}

// MarshalJSON custom Marsh for Float64Str
func (i Float64Str) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatFloat(float64(i), 'f', 3, 64))
}

// UnmarshalJSON custom UnMarsh for Float64Str
func (i *Float64Str) UnmarshalJSON(b []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		value, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*i = Float64Str(value)
		return nil
	}

	// Fallback to number
	return json.Unmarshal(b, (*float64)(i))
}

//TODO add code to get pitch error value
//refer machine_parser.js getAngleWithPitchError line# 359

// type SettingsRoot struct {
// 	DriveSettings DriverSettings `json:"X"`
// }

// SettingsRoot root struct of the settings
type SettingsRoot map[string]DriverSettings

var settingsRoot SettingsRoot

// settingsMutex guards every read and write of settingsRoot.
//
// We use sync.RWMutex (not sync.Mutex) because reads vastly outnumber writes
// in normal operation: every motion command calls GetDriverSettings, while
// writes only happen on LoadDriverSettings (startup) and SaveHomingReference
// (rare operator action). RWMutex lets concurrent readers proceed without
// blocking each other and only serializes when a writer is active.
//
// PREVIOUS BUG: SaveHomingReference mutated settingsRoot but GetDriverSettings,
// GetAllSettings, and LoadDriverSettings did not guard it at all. Concurrent
// reads happening during a write are a Go map data race — the runtime panics
// with "fatal error: concurrent map read and map write" and the process dies.
var settingsMutex sync.RWMutex

// LoadDriverSettings Load driver settings from settings.json file.
//
// Writes settingsRoot — must hold the write lock. We unmarshal into a
// temporary local first and only assign to settingsRoot under the lock,
// so a concurrent reader can never observe a half-populated map.
// stringTaggedNumericFields lists every DriverSettings field tagged with
// Go's `,string` json option — encoding/json requires the RAW JSON value
// for these to be a QUOTED string (e.g. "jog_feed": "20"), and returns an
// unmarshal error for that field if the JSON instead has a bare/unquoted
// number (e.g. "jog_feed": 20). Different writers of settings.json over
// this project's history have not been consistent about quoting these
// values (SaveHomingReference's own comment documents hitting this exact
// class of bug for work_offset/jog_feed and fixed it locally there via
// json.RawMessage — but LoadDriverSettings itself was never made tolerant
// of the same inconsistency).
//
// REAL BUG THIS FIXES: on a real rig, jog_feed was set to a bare `20` in
// settings.json. LoadDriverSettings's plain json.Unmarshal into the
// strongly-typed SettingsRoot struct failed to populate that field (it
// silently stayed at its zero value, 0), which meant every ManualJog
// command computed a velocity of exactly 0 — jog appeared to do nothing on
// real hardware even with the UI/settings.json apparently configured
// correctly.
var stringTaggedNumericFields = []string{
	"work_offset", "jog_feed", "homing_offset", "timing", "back_lash",
	"g55", "g54", "not", "g57", "g58", "cldl_timing", "pot", "g56",
}

// bareNumberPattern matches `"field_name": 20` or `"field_name":-3.5` etc —
// a JSON key followed by an UNQUOTED number — but does NOT match
// `"field_name": "20"`, which is already correctly quoted and left alone.
func bareNumberPattern(field string) *regexp.Regexp {
	return regexp.MustCompile(`"` + field + `"\s*:\s*(-?\d+(?:\.\d+)?)([,}\s])`)
}

// normalizeStringTaggedNumbers quotes any bare (unquoted) numeric values
// for the known ,string-tagged DriverSettings fields, so LoadDriverSettings
// can unmarshal settings.json regardless of which format a given entry was
// written in. Fields that are already correctly quoted, or absent, are
// left completely untouched.
func normalizeStringTaggedNumbers(raw []byte) []byte {
	out := raw
	for _, field := range stringTaggedNumericFields {
		out = bareNumberPattern(field).ReplaceAll(out, []byte(`"`+field+`": "$1"$2`))
	}
	return out
}

func LoadDriverSettings() error {
	path := helper.AppendWDPath("/settings/settings.json")
	settingsFile, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	settingsFile = normalizeStringTaggedNumbers(settingsFile)
	var parsed SettingsRoot
	err = json.Unmarshal(settingsFile, &parsed)

	// REAL BUG FIX: previously, ANY unmarshal error here caused an early
	// return WITHOUT ever assigning settingsRoot = parsed — discarding the
	// entire settings object even though Go's json.Unmarshal actually
	// continues past a type-mismatch on one field and correctly populates
	// every other field in the struct (verified directly against a real
	// settings.json that had one malformed field, binary_pos_feed's
	// feed_rate — every other field, including jog_feed, POT, NOT, and
	// pitch_error, decoded correctly, but the whole result was thrown away
	// because only "was there an error at all" was checked, not "did the
	// fields I actually need come through."
	//
	// This single bug caused two completely different-looking symptoms on
	// real hardware: JogFeed reading 0 despite settings.json correctly
	// having "20" (jog appeared dead), and a hard crash in
	// motordriver.getPitchError indexing into an empty PitchError slice —
	// both were really "the entire settings object silently failed to
	// load," not two separate bugs.
	//
	// Fix: adopt whatever Go successfully parsed regardless of whether an
	// error was also returned, and log loudly so a genuinely bad
	// settings.json is still visible — but a single malformed field no
	// longer takes every other correctly-formatted field down with it.
	if err != nil {
		fmt.Println("⚠️ LoadDriverSettings: settings.json parsed with errors "+
			"(some fields may be missing/zero-valued), but applying "+
			"everything that DID parse correctly rather than discarding it all:", err)
	}
	if parsed == nil {
		// Only bail out completely if Unmarshal produced nothing usable at
		// all (e.g. the file isn't valid JSON syntax whatsoever) — a
		// genuinely empty result is still worse than keeping stale settings.
		return err
	}

	settingsMutex.Lock()
	settingsRoot = parsed
	settingsMutex.Unlock()

	//if ECS finish timing is not specified then default to 500ms
	// if driverSettings.ECSFinTiming <= 0 {
	// 	driverSettings.ECSFinTiming = 500000
	// }
	channels.NotifyMotorDriver("SETTINGS_CHANGED", "", "", 0)
	return err
}

// GetAllSettings returns a SHALLOW COPY of the settings map.
//
// Previously this returned the live map by reference, which meant any caller
// iterating the result was racing against LoadDriverSettings / SaveHomingReference.
// Returning a copy means callers can iterate safely without holding our lock.
//
// Note: DriverSettings contains slice fields (PitchError, BinaryPosFeeds).
// The slice HEADERS in the returned values are copies, but they share the
// same backing arrays as the originals. Since neither LoadDriverSettings nor
// SaveHomingReference mutate slice contents in-place (they only reassign
// whole DriverSettings values into the map), this is safe today. If a future
// "edit a single pitch-error entry in place" code path is added, it will need
// to clone the slice before writing.
func GetAllSettings() map[string]DriverSettings {
	settingsMutex.RLock()
	defer settingsMutex.RUnlock()

	out := make(map[string]DriverSettings, len(settingsRoot))
	for k, v := range settingsRoot {
		out[k] = v
	}
	return out
}

// SaveHomingReference persists the absolute encoder position recorded at the
// end of a successful zero-reference move into settings.json for the given
// drive (e.g. "A", "X").
//
// Uses atomic write (tmp file + rename) so a power loss mid-write never
// corrupts the settings file — same pattern as rs232.go / SaveRS232Data().
//
// FIX: Use map[string]json.RawMessage for the read-modify-write instead of
// unmarshalling into SettingsRoot. The existing settings.json has fields
// tagged with ,string (e.g. work_offset, jog_feed) which expect quoted JSON
// strings. Unmarshalling a file that has bare numbers for those fields into
// SettingsRoot triggers "invalid use of ,string struct tag" errors even when
// those fields are not being modified. By treating each drive's value as
// RawMessage we bypass all type coercion — only the homing_apos key is
// touched; every other byte in the file is preserved verbatim.
func SaveHomingReference(driveName string, apos int32) error {
	path := helper.AppendWDPath("/settings/settings.json")

	raw, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	// Parse as raw map — avoids ,string tag coercion issues entirely.
	// Each drive's settings blob is kept as a raw JSON byte slice.
	var rootRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rootRaw); err != nil {
		return err
	}

	// Parse only the target drive's blob into a plain map so we can
	// set/update homing_apos without touching any other field.
	driveBlob, ok := rootRaw[driveName]
	if !ok {
		return fmt.Errorf("SaveHomingReference: drive %q not found in settings.json", driveName)
	}

	var driveMap map[string]json.RawMessage
	if err := json.Unmarshal(driveBlob, &driveMap); err != nil {
		return err
	}

	// Write homing_apos as a plain JSON number — no ,string wrapping.
	aposBytes, err := json.Marshal(apos)
	if err != nil {
		return err
	}
	driveMap["homing_apos"] = json.RawMessage(aposBytes)
	driveMap["homing_valid"] = json.RawMessage("true")

	// Re-encode the drive blob and put it back into the root map.
	newDriveBlob, err := json.Marshal(driveMap)
	if err != nil {
		return err
	}
	rootRaw[driveName] = json.RawMessage(newDriveBlob)

	// Update in-memory cache so GetDriverSettings() returns the new value
	// immediately without a full LoadDriverSettings() reload.
	//
	// Update only the HomingApos field on the EXISTING cached entry rather
	// than re-unmarshaling the whole drive blob into a fresh struct. The
	// re-unmarshal approach silently failed whenever ANY other field in
	// the blob had a type mismatch (confirmed on real settings.json:
	// work_offset/g55/g57/g58/g56 are stored as bare integers, but their
	// struct tags are `,string` and require quoted values) — the file
	// write below would still succeed and this function would still
	// return nil, but the in-memory cache silently kept the stale
	// HomingApos until the next full process restart. Updating only the
	// one field we actually changed cannot fail this way.
	//
	// Guarded by settingsMutex — this is the only place besides
	// LoadDriverSettings that writes to settingsRoot.
	settingsMutex.Lock()
	if settingsRoot == nil {
		settingsRoot = make(SettingsRoot)
	}
	existing := settingsRoot[driveName]
	existing.HomingApos = apos
	existing.HomingValid = true
	settingsRoot[driveName] = existing
	settingsMutex.Unlock()

	out, err := json.MarshalIndent(rootRaw, "", "\t")
	if err != nil {
		return err
	}

	// Atomic write: write to .tmp then rename — prevents file corruption
	// on power loss (same pattern as SaveRS232Data in rs232.go).
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SaveHomingCalibration atomically persists the encoder origin captured after
// multiturn reset and the derived fixed machine-zero pulse target.
func SaveHomingCalibration(driveName string, encoderZeroApos, homingApos int32, driveXRatio ...int) error {
	path := helper.AppendWDPath("/settings/settings.json")
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	var rootRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rootRaw); err != nil {
		return err
	}
	driveBlob, ok := rootRaw[driveName]
	if !ok {
		return fmt.Errorf("SaveHomingCalibration: drive %q not found in settings.json", driveName)
	}

	var driveMap map[string]json.RawMessage
	if err := json.Unmarshal(driveBlob, &driveMap); err != nil {
		return err
	}
	originBytes, _ := json.Marshal(encoderZeroApos)
	homeBytes, _ := json.Marshal(homingApos)
	driveMap["encoder_zero_apos"] = json.RawMessage(originBytes)
	driveMap["homing_apos"] = json.RawMessage(homeBytes)
	if len(driveXRatio) > 0 && driveXRatio[0] > 0 {
		ratioBytes, _ := json.Marshal(driveXRatio[0])
		driveMap["homing_drive_x_ratio"] = json.RawMessage(ratioBytes)
	}
	driveMap["homing_valid"] = json.RawMessage("true")

	newDriveBlob, err := json.Marshal(driveMap)
	if err != nil {
		return err
	}
	rootRaw[driveName] = json.RawMessage(newDriveBlob)

	settingsMutex.Lock()
	if settingsRoot == nil {
		settingsRoot = make(SettingsRoot)
	}
	existing := settingsRoot[driveName]
	existing.EncoderZeroApos = encoderZeroApos
	existing.HomingApos = homingApos
	if len(driveXRatio) > 0 && driveXRatio[0] > 0 {
		existing.HomingDriveXRatio = driveXRatio[0]
	}
	existing.HomingValid = true
	settingsRoot[driveName] = existing
	settingsMutex.Unlock()

	out, err := json.MarshalIndent(rootRaw, "", "\t")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GetDriverSettings returns the settings for one drive by name.
//
// The returned DriverSettings is a value copy — callers cannot mutate the
// stored entry through it. Slice fields share backing arrays; see the
// note on GetAllSettings.
func GetDriverSettings(driverName string) DriverSettings {
	settingsMutex.RLock()
	defer settingsMutex.RUnlock()
	return settingsRoot[driverName]
}

//SetMode set mode whether running in ABS(absolute) or not
// func SetMode(mode string) {
// 	driverSettings.Mode = mode
// }

// GetWorkOffset get the workoff set configured in settings. workoffsets are like G55, G56 etc
func (ds *DriverSettings) GetWorkOffset() map[string]float64 {
	wrkOffset := make(map[string]float64)
	wrkOffset["G53"] = 0
	wrkOffset["G54"] = ds.G54
	wrkOffset["G55"] = ds.G55
	wrkOffset["G56"] = ds.G56
	wrkOffset["G57"] = ds.G57
	wrkOffset["G58"] = ds.G58
	return wrkOffset
}
