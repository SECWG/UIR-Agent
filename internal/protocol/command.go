package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const ProtocolProduct = "WG-IR-Platform"
const CommandEncoding = "base64"
const MaxRunWireBytes = 384 << 10
const MaxCommandBytes = 256 << 10

// Encoding belongs to transport; Run stays unchanged for execution and durable deduplication.
type EncodedRun struct {
	Type       string            `json:"type"`
	Product    string            `json:"product"`
	ID         string            `json:"task_id"`
	CommandB64 string            `json:"command_b64"`
	Timeout    int64             `json:"timeout_ms"`
	Expires    string            `json:"expires_at"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

func strictWireJSON(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8 command message")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid command message fields")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid trailing command data")
	}
	return nil
}

func DecodeRunMessage(raw []byte) (Run, error) {
	var wire EncodedRun
	if len(raw) > MaxRunWireBytes {
		return Run{}, errors.New("command message too large")
	}
	if err := strictWireJSON(raw, &wire); err != nil {
		return Run{}, err
	}
	if wire.Type != "run" || wire.Product != ProtocolProduct || !uuidPattern.MatchString(wire.ID) {
		return Run{}, errors.New("invalid command message envelope")
	}
	if wire.CommandB64 == "" || len(wire.CommandB64) > base64.StdEncoding.EncodedLen(MaxCommandBytes) {
		return Run{}, errors.New("invalid encoded command size")
	}
	content, err := base64.StdEncoding.Strict().DecodeString(wire.CommandB64)
	if err != nil || len(content) > MaxCommandBytes || base64.StdEncoding.EncodeToString(content) != wire.CommandB64 {
		return Run{}, errors.New("invalid command Base64")
	}
	var command *Command
	if err := strictWireJSON(content, &command); err != nil {
		return Run{}, err
	}
	if command == nil {
		return Run{}, errors.New("command must be an object")
	}
	return Run{
		Type:    wire.Type,
		ID:      wire.ID,
		Command: *command,
		Timeout: wire.Timeout,
		Expires: wire.Expires,
		Cwd:     wire.Cwd,
		Env:     wire.Env,
	}, nil
}
