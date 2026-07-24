package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// DefaultEnvFiles are loaded in order. Later files override earlier file values.
// Keys already set in the process environment before load are never overwritten
// (shell / CI wins).
var DefaultEnvFiles = []string{".env", ".env.local"}

// LoadDotEnvFiles reads KEY=VALUE lines from paths.
// Missing files are skipped.
func LoadDotEnvFiles(paths ...string) (loaded []string, err error) {
	if len(paths) == 0 {
		paths = DefaultEnvFiles
	}
	preexisting := map[string]struct{}{}
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i > 0 {
			preexisting[e[:i]] = struct{}{}
		}
	}
	for _, path := range paths {
		n, e := loadOneDotEnv(path, preexisting)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return loaded, fmt.Errorf("%s: %w", path, e)
		}
		if n > 0 {
			loaded = append(loaded, path)
		}
	}
	return loaded, nil
}

func loadOneDotEnv(path string, preexisting map[string]struct{}) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, val, ok := splitEnvLine(line)
		if !ok {
			return n, fmt.Errorf("invalid line %d", lineNo)
		}
		if _, exists := preexisting[key]; exists {
			continue // shell / CI wins
		}
		if err := os.Setenv(key, val); err != nil {
			return n, err
		}
		n++
	}
	return n, sc.Err()
}

func splitEnvLine(line string) (key, val string, ok bool) {
	i := strings.IndexByte(line, '=')
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	val = strings.TrimSpace(line[i+1:])
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
	}
	return key, val, true
}
