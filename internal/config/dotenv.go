package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadDotEnv reads simple KEY=VALUE lines from path and exports them via
// os.Setenv, but ONLY for keys that are not already set in the process
// environment — so an explicit `LITELLM_API_KEY=… ./linklore` always wins
// over the .env file. Returns nil silently if the file does not exist
// (the .env is optional by design).
//
// Supported syntax (kept deliberately tiny — no godotenv dep):
//   - blank lines and lines starting with '#' are ignored
//   - "export KEY=VALUE" is accepted (the bash idiom)
//   - VALUE may be wrapped in single or double quotes; quotes are stripped
//   - inline comments after a value are NOT stripped (use quotes if your
//     value contains a '#')
//   - no variable interpolation inside VALUE — keep it boring
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			return fmt.Errorf("%s:%d: missing '=' in %q", path, lineNo, raw)
		}
		key := strings.TrimSpace(raw[:eq])
		val := strings.TrimSpace(raw[eq+1:])
		// Strip a single matching pair of surrounding quotes.
		if len(val) >= 2 {
			first, last := val[0], val[len(val)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		// Process env always wins — .env is for "if you don't set it elsewhere".
		if _, ok := os.LookupEnv(key); ok {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("%s:%d: setenv %s: %w", path, lineNo, key, err)
		}
	}
	return sc.Err()
}
