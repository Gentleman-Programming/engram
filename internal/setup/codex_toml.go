package setup

import (
	"fmt"
	"strconv"
	"strings"
)

// codexTOMLSpan is a complete physical expression, not a parsed TOML value.
// The scanner only establishes safe edit boundaries; it is not a TOML validator.
type codexTOMLSpan struct{ start, end, equal, codeEnd int }

func scanCodexTOML(s string) ([]codexTOMLSpan, error) {
	var spans []codexTOMLSpan
	start, equal, codeEnd := 0, -1, -1
	var stack []byte
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'', '"':
			end, err := codexStringEnd(s, i)
			if err != nil {
				return nil, err
			}
			i = end
			continue
		case '#':
			if len(stack) == 0 && codeEnd < 0 {
				codeEnd = i
			}
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		case '[', '{':
			stack = append(stack, s[i])
		case ']', '}':
			if len(stack) == 0 || (s[i] == ']' && stack[len(stack)-1] != '[') || (s[i] == '}' && stack[len(stack)-1] != '{') {
				return nil, fmt.Errorf("unbalanced delimiter at byte %d", i)
			}
			stack = stack[:len(stack)-1]
		case '=':
			if len(stack) == 0 && equal < 0 {
				equal = i
			}
		case '\n':
			if len(stack) == 0 {
				end := codeEnd
				if end < 0 {
					end = i
				}
				spans = append(spans, codexTOMLSpan{start, i + 1, equal, end})
				start, equal, codeEnd = i+1, -1, -1
			}
		}
		i++
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("unterminated container")
	}
	if start < len(s) {
		if codeEnd < 0 {
			codeEnd = len(s)
		}
		spans = append(spans, codexTOMLSpan{start, len(s), equal, codeEnd})
	}
	return spans, nil
}

// Strings are opaque: delimiters, comments, and assignment-shaped text inside
// them must never participate in configuration edits.
func codexStringEnd(s string, start int) (int, error) {
	q := s[start]
	width := 1
	if strings.HasPrefix(s[start:], strings.Repeat(string(q), 3)) {
		width = 3
	}
	for i := start + width; i < len(s); {
		if s[i] == q {
			n := 1
			for i+n < len(s) && s[i+n] == q {
				n++
			}
			if width == 1 {
				return i + 1, nil
			}
			if n >= 3 {
				if n > 5 {
					return 0, fmt.Errorf("unsupported quote run at byte %d", i)
				}
				return i + n, nil
			}
			i += n
			continue
		}
		if width == 1 && (s[i] == '\n' || s[i] == '\r') {
			return 0, fmt.Errorf("unterminated string at byte %d", start)
		}
		if q == '"' && s[i] == '\\' {
			i++
			if i >= len(s) {
				break
			}
			if width == 1 && (s[i] == '\n' || s[i] == '\r') {
				return 0, fmt.Errorf("unsupported string continuation at byte %d", i)
			}
		}
		i++
	}
	return 0, fmt.Errorf("unterminated string at byte %d", start)
}

// Decode only the key-path grammar needed to distinguish root assignments and
// table headers. Values remain raw bytes; unsupported keys fail closed.
func codexTOMLKey(s string) ([]string, error) {
	var parts []string
	for {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("empty key")
		}
		var key string
		if s[0] == '\'' || s[0] == '"' {
			end, err := codexStringEnd(s, 0)
			if err != nil {
				return nil, err
			}
			raw := s[:end]
			if strings.HasPrefix(raw, "\"\"\"") || strings.HasPrefix(raw, "'''") {
				return nil, fmt.Errorf("multiline key is unsupported")
			}
			if raw[0] == '\'' {
				key = raw[1 : len(raw)-1]
			} else {
				// Go's quoted-string decoder accepts additional escapes that TOML does
				// not. Reject those before decoding TOML's common escape subset.
				for i := 1; i < len(raw)-1; i++ {
					if raw[i] == '\\' {
						i++
						if !strings.ContainsRune("\"\\btnfruU", rune(raw[i])) {
							return nil, fmt.Errorf("unsupported key escape")
						}
					}
				}
				key, err = strconv.Unquote(raw)
				if err != nil {
					return nil, fmt.Errorf("unsupported quoted key: %w", err)
				}
			}
			s = s[end:]
		} else {
			end := 0
			for end < len(s) {
				c := s[end]
				if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
					break
				}
				end++
			}
			if end == 0 {
				return nil, fmt.Errorf("unsupported key")
			}
			key, s = s[:end], s[end:]
		}
		parts = append(parts, key)
		s = strings.TrimSpace(s)
		if s == "" {
			return parts, nil
		}
		if s[0] != '.' {
			return nil, fmt.Errorf("unsupported key suffix")
		}
		s = s[1:]
	}
}

func codexTOMLHeader(code string) ([]string, error) {
	code = strings.TrimSpace(code)
	if strings.HasPrefix(code, "[[") && strings.HasSuffix(code, "]]") {
		return codexTOMLKey(code[2 : len(code)-2])
	}
	if strings.HasPrefix(code, "[") && strings.HasSuffix(code, "]") {
		return codexTOMLKey(code[1 : len(code)-1])
	}
	return nil, fmt.Errorf("unsupported table header")
}

func removeTopLevelTOMLKey(content, key string) (string, error) {
	bom, s := splitCodexBOM(content)
	spans, err := scanCodexTOML(s)
	if err != nil {
		return "", err
	}
	var kept strings.Builder
	inTable := false
	for _, span := range spans {
		code := strings.TrimSpace(s[span.start:span.codeEnd])
		if strings.HasPrefix(code, "[") {
			if _, err := codexTOMLHeader(code); err != nil {
				return "", err
			}
			inTable = true
		}
		if !inTable && span.equal >= 0 {
			keys, err := codexTOMLKey(s[span.start:span.equal])
			if err != nil {
				return "", err
			}
			if len(keys) == 1 && keys[0] == key {
				value := strings.TrimSpace(s[span.equal+1 : span.codeEnd])
				if value == "" || value[0] != '\'' && value[0] != '"' {
					return "", fmt.Errorf("unsupported non-string value for %s", key)
				}
				end, err := codexStringEnd(value, 0)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(value[end:]) != "" {
					return "", fmt.Errorf("unsupported value suffix for %s", key)
				}
				continue
			}
		}
		kept.WriteString(s[span.start:span.end])
	}
	return bom + kept.String(), nil
}

func upsertCodexEngramBlock(content, command string) (string, error) {
	spans, err := scanCodexTOML(content)
	if err != nil {
		return "", err
	}
	var kept strings.Builder
	removing := false
	var table []string
	for _, span := range spans {
		code := strings.TrimSpace(content[span.start:span.codeEnd])
		if strings.HasPrefix(code, "[") {
			keys, err := codexTOMLHeader(code)
			if err != nil {
				return "", err
			}
			table = keys
			removing = len(keys) == 2 && keys[0] == "mcp_servers" && keys[1] == "engram"
			if strings.HasPrefix(code, "[[") && (removing || len(keys) == 1 && keys[0] == "mcp_servers") {
				return "", fmt.Errorf("unsupported MCP array table")
			}
		} else if span.equal >= 0 && (len(table) == 0 || len(table) == 1 && table[0] == "mcp_servers") {
			keys, err := codexTOMLKey(content[span.start:span.equal])
			if err != nil {
				return "", err
			}
			path := append(append([]string(nil), table...), keys...)
			if path[0] == "mcp_servers" && (len(path) == 1 || path[1] == "engram") {
				// Dotted keys and inline tables can already define this table.
				// Do not append a conflicting header or convert user syntax.
				return "", fmt.Errorf("unsupported dotted/inline MCP registration; use [mcp_servers.engram]")
			}
		}
		if !removing {
			kept.WriteString(content[span.start:span.end])
		}
	}
	base := kept.String()
	eol := codexLineEnding(content)
	if strings.TrimSpace(base) == "" {
		base = ""
	} else {
		if !strings.HasSuffix(base, eol) {
			base += eol
		}
		if !strings.HasSuffix(base, eol+eol) {
			base += eol
		}
	}
	return base + strings.ReplaceAll(codexEngramBlockStr(command), "\n", eol) + eol, nil
}

func codexLineEnding(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 && s[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}
