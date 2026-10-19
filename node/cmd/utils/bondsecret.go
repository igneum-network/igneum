package utils

import (
	"fmt"
	"os"
	"strings"
)

// secretFromFlags is the value of a secret-bearing option given either on
// the command line (the string flag, kept for the lab and the tests) or in
// a file the owner alone can read (the file flag; F-440, 17 October 2026:
// the seeds and bond keys of every sealer of record rode argv, readable
// through /proc by every user of the box). The two together are refused by
// name; a file that is group- or world-readable, missing or unreadable is
// refused by its path; the value is read once, trimmed, and never appears
// in an error. The caller parses it exactly as the string form.
func secretFromFlags(value, file, valueName, fileName string) (string, error) {
	switch {
	case value != "" && file != "":
		return "", fmt.Errorf("Options %q and %q are mutually exclusive", valueName, fileName)
	case file == "":
		return value, nil
	}
	st, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("Option %q: %s: %v", fileName, file, err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("Option %q: %s is not a regular file", fileName, file)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("Option %q: %s must be readable by its owner alone (mode 0600)", fileName, file)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("Option %q: %s: %v", fileName, file, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("Option %q: %s is empty", fileName, file)
	}
	return v, nil
}
