package clientstate

import (
	"encoding/json"
	"errors"
	"strings"
)

// A duplicated pending field can otherwise decode as null and erase the first
// saved intent. Refuse ambiguous local state before binding it to a schema.
func distinctStateKeys(d *json.Decoder, depth int, schemaKeys bool) error {
	if depth > 64 {
		return errors.New("client place nesting exceeds safe limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		childSchema := true
		if delim == '{' {
			token, err = d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] || schemaKeys && key != strings.ToLower(key) {
				return errors.New("client place has duplicate or invalid field")
			}
			seen[key] = true
			// Places is the sole dynamic JSON object. Its case-sensitive stable
			// group IDs are data, while each contained Place has schema fields.
			childSchema = !(depth == 0 && key == "places")
		}
		if err := distinctStateKeys(d, depth+1, childSchema); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
