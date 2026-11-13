package helper

import "math"

//RoundFloat round float to the nearest value
func RoundFloat(num float64, precision int) float64 {
	output := math.Pow(10, float64(precision))
	return float64(round(num*output)) / output
}

func round(num float64) int {
	return int(num + math.Copysign(0.5, num))
}

//RoundFloatTo3 round the decimal precision to 3
func RoundFloatTo3(num float64) float64 {
	return RoundFloat(num, 3)
}
