package util

func ToExitVal(err error) int {
	if err != nil {
		return 1
	}
	return 0
}
