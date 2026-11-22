package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/SECWG/UIR-Agent/internal/protocol"
)

var errShutdown = errors.New("platform requested agent exit")

func validateShutdown(raw []byte) error {
	var request struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Type != "shutdown_request" || !protocol.ValidID(request.ID) || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid agent exit request")
	}
	return nil
}
