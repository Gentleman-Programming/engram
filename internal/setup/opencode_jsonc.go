package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// jsoncObject retains source offsets, not a reserialized representation.
// Non-object values have a nil members map.
type jsoncObject struct {
	open        int
	members     map[string]*jsoncObject
	values      []*jsoncObject
	stringStart int
	stringEnd   int
}

// parseOpenCodeJSONC validates the entire document before any edit. Comments
// and trailing commas are masked with spaces so decoder offsets still refer
// to the original bytes. Duplicate keys are unsafe, including escaped aliases.
func parseOpenCodeJSONC(data []byte) (*jsoncObject, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	normalized := bytes.Clone(data)
	inString := false
	for i := 0; i < len(normalized); i++ {
		if inString {
			switch normalized[i] {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		if normalized[i] == '"' {
			inString = true
			continue
		}
		if normalized[i] != '/' || i+1 >= len(normalized) {
			continue
		}
		start := i
		switch normalized[i+1] {
		case '/':
			i += 2
			for i < len(normalized) && normalized[i] != '\n' && normalized[i] != '\r' {
				i++
			}
		case '*':
			i += 2
			for i+1 < len(normalized) && (normalized[i] != '*' || normalized[i+1] != '/') {
				i++
			}
			if i+1 >= len(normalized) {
				return nil, fmt.Errorf("unterminated block comment")
			}
			i += 2
		default:
			continue
		}
		for j := start; j < i; j++ {
			if normalized[j] != '\r' && normalized[j] != '\n' {
				normalized[j] = ' '
			}
		}
		i--
	}
	inString = false
	for i := 0; i < len(normalized); i++ {
		if inString {
			switch normalized[i] {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		if normalized[i] == '"' {
			inString = true
			continue
		}
		if normalized[i] != ',' {
			continue
		}
		next := i + 1
		for next < len(normalized) && jsoncSpace(normalized[next]) {
			next++
		}
		prev := i - 1
		for prev >= 0 && jsoncSpace(normalized[prev]) {
			prev--
		}
		if next < len(normalized) && (normalized[next] == '}' || normalized[next] == ']') && prev >= 0 && !bytes.ContainsRune([]byte("{[,:"), rune(normalized[prev])) {
			normalized[i] = ' '
		}
	}
	var raw json.RawMessage
	if err := json.Unmarshal(normalized, &raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	return readJSONCValue(decoder, normalized, 0)
}

func jsoncSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

func readJSONCValue(decoder *json.Decoder, normalized []byte, depth int) (*jsoncObject, error) {
	if depth > 1000 {
		return nil, fmt.Errorf("JSONC nesting too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	node := &jsoncObject{stringStart: -1}
	delimiter, ok := token.(json.Delim)
	if !ok {
		if _, ok := token.(string); ok {
			node.stringEnd = int(decoder.InputOffset())
			node.stringStart = jsoncStringStart(normalized, node.stringEnd)
		}
		return node, nil
	}
	if delimiter == '{' {
		node.open = int(decoder.InputOffset()) - 1
		node.members = make(map[string]*jsoncObject)
	}
	for decoder.More() {
		key := ""
		if delimiter == '{' {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key = token.(string) // Syntax has already been validated by json.Unmarshal.
			if _, exists := node.members[key]; exists {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
		}
		child, err := readJSONCValue(decoder, normalized, depth+1)
		if err != nil {
			return nil, err
		}
		if delimiter == '{' {
			node.members[key] = child
		} else {
			node.values = append(node.values, child)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return node, nil
}

func jsoncStringStart(data []byte, end int) int {
	for i := end - 2; i >= 0; i-- {
		if data[i] != '"' {
			continue
		}
		escaped := false
		for backslash := i - 1; backslash >= 0 && data[backslash] == '\\'; backslash-- {
			escaped = !escaped
		}
		if !escaped {
			return i
		}
	}
	return -1
}

func jsoncString(data []byte, node *jsoncObject) (string, bool) {
	if node == nil || node.stringStart < 0 || node.stringEnd <= node.stringStart {
		return "", false
	}
	var value string
	if err := json.Unmarshal(data[node.stringStart:node.stringEnd], &value); err != nil {
		return "", false
	}
	return value, true
}

func replaceJSONCString(data []byte, node *jsoncObject, value []byte) ([]byte, error) {
	if node == nil || node.stringStart < 0 || node.stringEnd <= node.stringStart || node.stringEnd > len(data) {
		return nil, fmt.Errorf("expected string")
	}
	output := make([]byte, 0, len(data)+len(value)-(node.stringEnd-node.stringStart))
	output = append(output, data[:node.stringStart]...)
	output = append(output, value...)
	return append(output, data[node.stringEnd:]...), nil
}

// insertJSONCMember inserts before the first original member. This avoids
// relocating comments or needing to rewrite an existing trailing comma.
func insertJSONCMember(data []byte, object *jsoncObject, member []byte) []byte {
	if len(object.members) != 0 {
		member = append(member, ',')
	}
	offset := object.open + 1
	output := make([]byte, 0, len(data)+len(member))
	output = append(output, data[:offset]...)
	output = append(output, member...)
	return append(output, data[offset:]...)
}
