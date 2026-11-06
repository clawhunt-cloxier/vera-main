package app

import "os"

// filepathStat returns isDir, or an error when missing.
func filepathStat(p string) (bool, error) {
	st, err := os.Stat(p)
	if err != nil {
		return false, err
	}
	return st.IsDir(), nil
}
