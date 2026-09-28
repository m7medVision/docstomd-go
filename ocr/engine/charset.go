package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// loadCharset reads the recognizer's character list.
func loadCharset(dir string, c Charset) ([]string, error) {
	if c.JSON != "" {
		data, err := os.ReadFile(filepath.Join(dir, c.JSON))
		if err != nil {
			return nil, err
		}
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			return nil, fmt.Errorf("%s: %v", filepath.Join(dir, c.JSON), err)
		}
		key := c.Key
		if key == "" {
			key = "vocab"
		}
		vocab, ok := obj[key].(string)
		if !ok || vocab == "" {
			return nil, fmt.Errorf("%s: no string %q", filepath.Join(dir, c.JSON), key)
		}
		var chars []string
		for _, r := range vocab {
			chars = append(chars, string(r))
		}
		return chars, nil
	}
	if c.File != "" {
		data, err := os.ReadFile(filepath.Join(dir, c.File))
		if err != nil {
			return nil, err
		}
		var chars []string
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			chars = append(chars, strings.TrimSuffix(line, "\r"))
		}
		return chars, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, c.YAML))
	if err != nil {
		return nil, err
	}
	key := c.Key
	if key == "" {
		key = "character_dict"
	}
	chars, err := yamlList(data, key)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Join(dir, c.YAML), err)
	}
	return chars, nil
}

// yamlList reads the block sequence under the first mapping key named key:
//
//	character_dict:
//	- a
//	- 'b'
//
// It supports the scalar forms PaddleOCR's exported configs use: plain,
// single-quoted (a doubled quote escapes a quote) and double-quoted with JSON-style
// escapes. It is not a general YAML parser.
func yamlList(data []byte, key string) ([]string, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	var (
		found  bool
		indent = -1
		out    []string
	)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimLeft(line, " ")
		lead := len(line) - len(trimmed)
		if !found {
			if strings.TrimSpace(trimmed) == key+":" {
				found = true
				indent = lead
			}
			continue
		}
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
			if lead <= indent {
				break
			}
			return nil, fmt.Errorf("unexpected line in %s: %q", key, line)
		}
		if lead < indent {
			break
		}
		value, err := yamlScalar(strings.TrimPrefix(strings.TrimPrefix(trimmed, "-"), " "))
		if err != nil {
			return nil, fmt.Errorf("%s item %d: %v", key, len(out)+1, err)
		}
		out = append(out, value)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("no %s list", key)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty %s list", key)
	}
	return out, nil
}

func yamlScalar(s string) (string, error) {
	switch {
	case strings.HasPrefix(s, "'"):
		if len(s) < 2 || !strings.HasSuffix(s, "'") {
			return "", fmt.Errorf("unterminated quote in %q", s)
		}
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	case strings.HasPrefix(s, `"`):
		v, err := strconv.Unquote(s)
		if err != nil {
			return "", fmt.Errorf("bad double-quoted scalar %q", s)
		}
		return v, nil
	}
	return s, nil
}
