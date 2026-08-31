package runtimecapture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Decode reads all versioned JSON values from a runtime capture stream.
func Decode(reader io.Reader) ([]Record, error) {
	records := make([]Record, 0)
	err := decodeEach(reader, func(record Record) {
		records = append(records, record)
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func decodeEach(reader io.Reader, visit func(Record)) error {
	if reader == nil {
		return fmt.Errorf("runtime capture input is nil")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	for index := 0; ; index++ {
		var record Record
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode runtime capture record %d: %w", index+1, err)
		}
		if err := record.Validate(); err != nil {
			return fmt.Errorf("validate runtime capture record %d: %w", index+1, err)
		}
		visit(record)
	}
}
