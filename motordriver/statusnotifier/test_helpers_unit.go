//go:build unit

package statusnotifier

// SetCurrentErrorCode is a test-only seam that directly sets the cached
// numeric error code (currentErrorCode) without going through DriverError().
// This lets unit tests set up state deterministically without triggering
// the notifier's channel broadcast (channels.BroadCastUIChannel is nil in
// unit tests and would block forever).
func SetCurrentErrorCode(code int) {
	currentErrorCode.Store(int32(code))
}
