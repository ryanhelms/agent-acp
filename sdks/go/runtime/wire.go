package runtime

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	model "agent-acp.local/sdks/go/model"
	"agent-acp.local/transport"
	"google.golang.org/protobuf/proto"
)

// Only selected, matched frames reach this splitter. The transport validated
// their JSON and routing. The payload never becomes a second metadata field.
func wireRecord(receipt *transport.Receipt, method string, limit int) (*model.WireProvenance, json.RawMessage) {
	if receipt == nil {
		return nil, nil
	}
	wire := &model.WireProvenance{Method: method, Kind: model.WireMessageKind_WIRE_MESSAGE_KIND_NOTIFICATION}
	if id := receipt.ID; id != nil {
		wire.Kind = model.WireMessageKind_WIRE_MESSAGE_KIND_REQUEST
		switch {
		case len(id) > limit:
			wire.OmittedIdBytes = uint64(len(id))
		case !utf8.Valid(id):
			wire.IdJsonBytes = bytes.Clone(id)
		default:
			wire.IdJson = proto.String(string(id))
		}
	}
	var body json.RawMessage
	var extras []byte
	extraSize := 0
	obj := bytes.TrimSpace(receipt.Envelope)
	members := bytes.TrimSpace(obj[1 : len(obj)-1])
	for len(members) > 0 {
		keyEnd := jsonStringEnd(members)
		key := members[:keyEnd]
		afterKey := bytes.TrimLeft(members[keyEnd:], " \t\r\n")
		rest := bytes.TrimLeft(afterKey[1:], " \t\r\n")
		end := jsonValueEnd(rest)
		value := rest[:end:end]
		name := key[1 : len(key)-1]
		if bytes.IndexByte(name, '\\') >= 0 {
			var decoded string
			_ = json.Unmarshal(key, &decoded)
			name = []byte(decoded)
		}
		switch string(name) {
		case "id", "method", "params":
		case "jsonrpc":
			if len(value) > min(4096, limit) {
				wire.OmittedProtocolVersionBytes = uint64(len(value))
			} else if !utf8.Valid(value) || len(value) == 0 || value[0] != '"' {
				wire.ProtocolVersionJsonBytes = bytes.Clone(value)
			} else {
				var version string
				_ = json.Unmarshal(value, &version)
				wire.ProtocolVersion = &version
			}
		case "result", "error":
			wire.Kind = model.WireMessageKind_WIRE_MESSAGE_KIND_RESULT
			if string(name) == "error" {
				wire.Kind = model.WireMessageKind_WIRE_MESSAGE_KIND_ERROR
			}
			body = value
			if len(value) <= limit {
				// A small payload must not pin a large frame in the window.
				body = bytes.Clone(value)
			}
		default:
			// Preserve the original member spelling, order, and spacing.
			// Count even once oversized, without copying its nested values.
			member := members[:len(members)-len(rest)+end]
			if extraSize == 0 {
				extraSize = 1 // final closing brace
			}
			extraSize += 1 + len(member)
			if extraSize <= limit {
				if len(extras) == 0 {
					extras = append(extras, '{')
				} else {
					extras = append(extras, ',')
				}
				extras = append(extras, member...)
			} else {
				extras = nil
			}
		}
		members = bytes.TrimLeft(rest[end:], " \t\r\n")
		if len(members) > 0 {
			members = bytes.TrimLeft(members[1:], " \t\r\n")
		}
	}
	if extraSize > limit {
		wire.OmittedAdditionalFieldsBytes = uint64(extraSize)
	} else if len(extras) > 0 {
		extras = append(extras, '}')
		if utf8.Valid(extras) {
			wire.AdditionalFieldsJson = string(extras)
		} else {
			wire.AdditionalFieldsJsonBytes = extras
		}
	}
	return wire, body
}

func omitWireExtras(wire *model.WireProvenance) {
	n := len(wire.AdditionalFieldsJson) + len(wire.AdditionalFieldsJsonBytes)
	if n != 0 {
		wire.OmittedAdditionalFieldsBytes = uint64(n)
		wire.AdditionalFieldsJson = ""
		wire.AdditionalFieldsJsonBytes = nil
	}
}

func omitWireID(wire *model.WireProvenance) {
	n := len(wire.GetIdJson()) + len(wire.IdJsonBytes)
	if n != 0 {
		wire.OmittedIdBytes = uint64(n)
		wire.IdJson = nil
		wire.IdJsonBytes = nil
	}
}
