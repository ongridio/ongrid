// Package resultanalysis streams authorized saved data into a bounded jq subprocess.
package resultanalysis

import (
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

const MaxInputBytes = 64 << 20
const MaxExpressionBytes = 16 << 10
const MaxOutputBytes = 64 << 10
const MaxHeaderBytes = 160 << 10
const MaxRecordBytes = 1 << 20

type Source struct {
	ID                string `json:"result_id"`
	Format            string `json:"format"`
	Pointer           string `json:"pointer,omitempty"`
	CollectionPointer string `json:"collection_pointer,omitempty"`
}

type Input struct {
	Source
	Bytes   int64     `json:"bytes"`
	Content io.Reader `json:"-"`
}

type Request struct {
	Expression string  `json:"expression"`
	Inputs     []Input `json:"inputs"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.Expression) == "" || len(r.Expression) > MaxExpressionBytes || len(r.Inputs) < 1 || len(r.Inputs) > 4 {
		return fmt.Errorf("analysis requires a jq expression (at most 16 KiB) and 1–4 saved results")
	}
	seen := map[string]bool{}
	var total int64
	for _, input := range r.Inputs {
		id, err := uuid.Parse(input.ID)
		if err != nil || id.String() != input.ID || seen[input.ID] {
			return fmt.Errorf("analysis input IDs must be distinct canonical UUIDs")
		}
		seen[input.ID] = true
		switch input.Format {
		case "json", "json_value", "json_text", "csv", "lines":
		default:
			return fmt.Errorf("input format must be json, json_value, json_text, csv or lines")
		}
		if input.CollectionPointer != "" && input.Format != "json_text" {
			return fmt.Errorf("collection_pointer is only for JSON inside a text string")
		}
		for _, pointer := range []string{input.Pointer, input.CollectionPointer} {
			if len(pointer) > 1024 || (pointer != "" && !strings.HasPrefix(pointer, "/")) {
				return fmt.Errorf("invalid JSON pointer")
			}
			for i := 0; i < len(pointer); i++ {
				if pointer[i] == '~' {
					i++
					if i == len(pointer) || (pointer[i] != '0' && pointer[i] != '1') {
						return fmt.Errorf("invalid JSON pointer escape")
					}
				}
			}
		}
		if input.Bytes < 0 || input.Bytes > MaxInputBytes-total {
			return fmt.Errorf("analysis inputs exceed 64 MiB")
		}
		total += input.Bytes
	}
	return nil
}
