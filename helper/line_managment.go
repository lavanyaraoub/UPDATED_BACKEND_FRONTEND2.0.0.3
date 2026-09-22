package helper


import (
    "fmt"
    "os"
    "strconv"
    "strings"
)

// ReadLastLine reads the last processed line number from a file.
func ReadLastLine(filename string) (int, error) {
    data, err := os.ReadFile(filename)
    // If the file doesn't exist, it's not an error. We just start from the beginning.
    if os.IsNotExist(err) {
        return 0, nil
    }
    if err != nil {
        return 0, err
    }

    content := strings.TrimSpace(string(data))
    if content == "" {
        return 0, nil
    }

    lastLine, err := strconv.Atoi(content)
    if err != nil {
        return 0, fmt.Errorf("corrupted last_line.txt file: %w", err)
    }
    // We return the last completed line + 1 to start from the next line.
    return lastLine + 1, nil
}

// WriteLastLine saves the last completed line number to a file.
func WriteLastLine(filename string, lineNum int) error {
    return os.WriteFile(filename, []byte(strconv.Itoa(lineNum)), 0644)
}