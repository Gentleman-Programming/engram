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
	open    int
	members map[string]*jsoncObject
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
	return readJSONCValue(decoder, 0)
}

func jsoncSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

func readJSONCValue(decoder *json.Decoder, depth int) (*jsoncObject, error) {
	if depth > 1000 {
		return nil, fmt.Errorf("JSONC nesting too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	node := &jsoncObject{}
	delimiter, ok := token.(json.Delim)
	if !ok {
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
		child, err := readJSONCValue(decoder, depth+1)
		if err != nil {
			return nil, err
		}
		if delimiter == '{' {
			node.members[key] = child
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return node, nil
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
