package helper

import (
	"fmt"
)

// IntToBinary returns the binary of the interger in string format in reversed order
// otherwise index of a location in binary will give wrong result.
// For e.g. inBinary[10] will start from left but in binary 10 postion should check from right
// When the binary is in reverse inBinary[10] is valid.
//
// FIX (June 2026): now uses uint32(value) for proper two's-complement output.
// Previously used fmt.Sprintf("%032b", value) which produced sign-magnitude
// format for negative inputs ("-000...001"), making binary[31]=='-' and
// breaking bit-position indexing. The uint32 cast fixes this:
//
//	IntToBinary(-1)  → "11111111111111111111111111111111" (0xFFFFFFFF)
//
// Current callers only pass non-negative uint16 statuswords so observable
// behaviour is unchanged for all existing call sites.
func IntToBinary(value int) string {
	// Use uint32 cast for proper two's-complement bit representation.
	// This fixes the previous sign-magnitude format (fmt.Sprintf("%032b", negative))
	// which produced a '-' prefix character rather than two's-complement bits,
	// breaking bit-position indexing for negative inputs.
	// Current callers only pass non-negative uint16 statuswords so the observable
	// behaviour is unchanged for all existing call sites.
	return reverse(fmt.Sprintf("%032b", uint32(value)))
}

// func bInt(n int64) string {
// 	return strconv.FormatUint(*(*uint64)(unsafe.Pointer(&n)), 2)
// }

func reverse(str string) (result string) {
	// fmt.Println("orig", str)
	for _, v := range str {
		result = string(v) + result
	}
	return

	// r := []rune(str)
	// for i, j := 0, len(r)-1; i < len(r)/2; i, j = i+1, j-1 {
	// 	r[i], r[j] = r[j], r[i]
	// }
	// return string(r)
}
