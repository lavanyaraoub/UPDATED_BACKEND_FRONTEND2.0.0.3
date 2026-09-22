//go:build unit

package settings

// SetDriverSettings injects a DriverSettings value for the given drive name.
// This is a test-only helper — it bypasses the JSON file loading path so that
// unit tests can supply deterministic settings without touching the filesystem
// or relying on a settings.json being present on the build machine.
//
// Thread-safe: acquires the write lock exactly as LoadDriverSettings does.
func SetDriverSettings(name string, ds DriverSettings) {
	settingsMutex.Lock()
	defer settingsMutex.Unlock()
	if settingsRoot == nil {
		settingsRoot = make(SettingsRoot)
	}
	settingsRoot[name] = ds
}
