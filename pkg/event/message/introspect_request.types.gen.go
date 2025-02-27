// Code generated by github.com/atombender/go-jsonschema, DO NOT EDIT.

package message

import "fmt"
import "encoding/json"

// Schema for the introspect repository kafka message
type IntrospectRequestMessage struct {
	// The base URL for the repository to be introspected
	Url string `json:"url"`

	// The UUID for the repository to be introspected. This
	// is used for the key field to distribute the messages
	// to the consumers.
	//
	Uuid string `json:"uuid"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (j *IntrospectRequestMessage) UnmarshalJSON(b []byte) error {
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if v, ok := raw["url"]; !ok || v == nil {
		return fmt.Errorf("field url: required")
	}
	if v, ok := raw["uuid"]; !ok || v == nil {
		return fmt.Errorf("field uuid: required")
	}
	type Plain IntrospectRequestMessage
	var plain Plain
	if err := json.Unmarshal(b, &plain); err != nil {
		return err
	}
	*j = IntrospectRequestMessage(plain)
	return nil
}
