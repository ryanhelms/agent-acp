package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ACPUpdateParamsSource identifies complete JSON params for a session/update
// carrying fields beyond sessionId and update.
const ACPUpdateParamsSource = "acp.session_update_params"

type updateParams struct {
	id     string
	update json.RawMessage
	extra  bool
	offset int
}

// Decode the two exact, case-sensitive routing fields while borrowing the inner
// value. The JSON-RPC transport owns raw for this frame. Validate it before
// walking its top-level members; nested keys can never route a notification.
func decodeUpdateParams(raw json.RawMessage) (updateParams, error) {
	var m updateParams
	if !json.Valid(raw) {
		return m, errors.New("invalid session/update JSON")
	}
	obj := bytes.TrimSpace(raw)
	if obj[0] != '{' {
		return m, errors.New("session/update params must be an object")
	}
	end := len(bytes.TrimRight(raw, " \t\r\n")) - 1
	members := bytes.TrimLeft(obj[1:len(obj)-1], " \t\r\n")
	var haveID, haveUpdate bool
	for len(members) > 0 {
		keyEnd := jsonStringEnd(members)
		key := updateRoutingKey(members[:keyEnd])
		afterKey := bytes.TrimLeft(members[keyEnd:], " \t\r\n")
		value := bytes.TrimLeft(afterKey[1:], " \t\r\n")
		valueEnd := jsonValueEnd(value)
		switch key {
		case "sessionId":
			if haveID || value[0] != '"' {
				return m, errors.New("session/update requires an unambiguous string sessionId")
			}
			haveID = true
			id := value[1 : valueEnd-1]
			if bytes.IndexByte(id, '\\') < 0 && utf8.Valid(id) {
				m.id = string(id)
			} else {
				var decoded string
				if err := json.Unmarshal(value[:valueEnd], &decoded); err != nil {
					return m, err
				}
				m.id = decoded
			}
		case "update":
			if haveUpdate {
				return m, errors.New("session/update has more than one update")
			}
			haveUpdate = true
			m.update = value[:valueEnd:valueEnd]
			// All suffixes end immediately before the outer closing brace.
			m.offset = end - len(value)
		default:
			m.extra = true
		}
		next := bytes.TrimLeft(value[valueEnd:], " \t\r\n")
		if len(next) == 0 {
			break
		}
		members = bytes.TrimLeft(next[1:], " \t\r\n")
	}
	return m, nil
}

func updateRoutingKey(key []byte) string {
	switch string(key) {
	case `"sessionId"`:
		return "sessionId"
	case `"update"`:
		return "update"
	}
	if bytes.IndexByte(key, '\\') >= 0 {
		var name string
		_ = json.Unmarshal(key, &name)
		return name
	}
	return ""
}

// These scanners consume only syntactically valid JSON. Strings may contain
// escaped quotes and brackets. They neither copy nor decode nested values.
func jsonStringEnd(b []byte) int {
	for start := 1; start < len(b); {
		quote := start + bytes.IndexByte(b[start:], '"')
		if quote < start {
			break
		}
		back := quote
		for back > 0 && b[back-1] == '\\' {
			back--
		}
		if (quote-back)%2 == 0 {
			return quote + 1
		}
		start = quote + 1
	}
	return len(b)
}

func jsonValueEnd(b []byte) int {
	switch b[0] {
	case '"':
		return jsonStringEnd(b)
	case '{', '[':
		depth := 0
		for i := 0; i < len(b); i++ {
			switch b[i] {
			case '"':
				i += jsonStringEnd(b[i:]) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
	default:
		if i := bytes.IndexAny(b, ", \t\r\n"); i >= 0 {
			return i
		}
	}
	return len(b)
}
