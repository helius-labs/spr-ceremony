package signing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Payload defines the signed wire format. Both native and browser verification
// use this Go implementation, including JSON number and string serialization.
// The manifest digest supplies the ceremony context; signatures cannot be moved
// between ceremonies or between invitation and receipt messages.
func Payload(kind, manifest string, message []byte) ([]byte, error) {
	if (kind != "invitation" && kind != "receipt") || len(manifest) != 64 || len(message) > MaxPayloadBytes {
		return nil, fmt.Errorf("invalid signed message")
	}
	for _, c := range manifest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil, fmt.Errorf("invalid manifest digest")
		}
	}
	var fields map[string]json.RawMessage
	d := json.NewDecoder(bytes.NewReader(message))
	if err := d.Decode(&fields); err != nil || fields == nil {
		return nil, fmt.Errorf("invalid signed JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid signed JSON")
	}
	delete(fields, "signature")
	// Decode again with UseNumber to canonicalize nested objects without losing
	// integer precision. encoding/json sorts every object by its UTF-8 keys.
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value any
	if err = d.Decode(&value); err != nil {
		return nil, err
	}
	b, err = json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append([]byte("SPR coordinator "+kind+" v1\n"+manifest+"\n"), append(b, '\n')...), nil
}

func VerifyMessage(public []byte, kind, manifest string, message []byte) error {
	var envelope struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil {
		return fmt.Errorf("invalid signed message")
	}
	payload, err := Payload(kind, manifest, message)
	if err != nil {
		return err
	}
	return Verify(public, payload, envelope.Signature)
}
