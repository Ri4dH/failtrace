package repro

import (
	"fmt"
	"strings"
)

// parseEnvFile parses a $GITHUB_ENV file: "NAME=value" lines and multiline
// values written as "NAME<<DELIMITER" ... "DELIMITER".
func parseEnvFile(data string) (map[string]string, error) {
	out := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		eq := strings.Index(line, "=")
		heredoc := strings.Index(line, "<<")
		if heredoc > 0 && (eq < 0 || heredoc < eq) {
			name, delim := line[:heredoc], line[heredoc+2:]
			var value []string
			closed := false
			for i++; i < len(lines); i++ {
				if lines[i] == delim {
					closed = true
					break
				}
				value = append(value, lines[i])
			}
			if !closed {
				return out, fmt.Errorf("GITHUB_ENV: missing delimiter %q for %s", delim, name)
			}
			out[name] = strings.Join(value, "\n")
			continue
		}
		if eq <= 0 {
			return out, fmt.Errorf("GITHUB_ENV: invalid line %q", line)
		}
		out[line[:eq]] = line[eq+1:]
	}
	return out, nil
}

// parsePathFile parses a $GITHUB_PATH file: one directory per line.
func parsePathFile(data string) []string {
	var out []string
	for _, line := range strings.Split(data, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
