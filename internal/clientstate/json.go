package clientstate

import (
	"encoding/json"
	"errors"
)

// A duplicated pending field can otherwise decode as null and erase the first
// saved intent. Refuse ambiguous local state before binding it to a schema.
func distinctStateKeys(d *json.Decoder, depth int) error {
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
		if delim == '{' {
			token, err = d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("client place has duplicate or invalid field")
			}
			seen[key] = true
		}
		if err := distinctStateKeys(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
