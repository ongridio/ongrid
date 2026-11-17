package resultanalysis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/itchyny/gojq"
)

var ErrOutputLimit = errors.New("analysis output exceeds 64 KiB; return a smaller summary")

type Coverage struct {
	ResultID    string `json:"result_id"`
	RecordsRead int64  `json:"records_read"`
	Complete    bool   `json:"complete"`
}

type output struct {
	Values   json.RawMessage `json:"values"`
	Coverage []Coverage      `json:"coverage"`
}

// Worker is entered before loading Manager configuration or starting services.
func Worker(in io.Reader, out io.Writer) error {
	if err := restrictMemory(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	r := bufio.NewReaderSize(in, MaxHeaderBytes+1)
	header, err := r.ReadSlice('\n')
	if err != nil || len(header) > MaxHeaderBytes {
		return fmt.Errorf("invalid or oversized analysis header")
	}
	var req Request
	decoder := json.NewDecoder(bytes.NewReader(header))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&req); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing analysis header")
	}
	if err = req.Validate(); err != nil {
		return err
	}
	for i := range req.Inputs {
		req.Inputs[i].Content = &io.LimitedReader{R: r, N: req.Inputs[i].Bytes}
	}
	return evaluate(ctx, req, out)
}

type inputIter struct {
	next func() (any, error, bool)
	err  error
}

func (i *inputIter) Next() (any, bool) {
	v, err, ok := i.next()
	if err != nil {
		i.err = err
		return err, true
	}
	return v, ok
}

func evaluate(ctx context.Context, req Request, out io.Writer) error {
	if err := req.Validate(); err != nil {
		return err
	}
	coverage := make([]Coverage, len(req.Inputs))
	for i, s := range req.Inputs {
		coverage[i].ResultID = s.ID
	}
	seq := func(yield func(any, error) bool) {
		for i, source := range req.Inputs {
			if source.Content == nil {
				yield(nil, fmt.Errorf("missing input stream"))
				return
			}
			err := streamSource(source.Content, source.Source, func(value any) bool {
				if err := ctx.Err(); err != nil {
					yield(nil, err)
					return false
				}
				coverage[i].RecordsRead++
				return yield(map[string]any{"source": source.ID, "value": value}, nil)
			})
			if errors.Is(err, errStopped) {
				return
			}
			if err == nil {
				if limited, ok := source.Content.(*io.LimitedReader); ok && limited.N != 0 {
					err = io.ErrUnexpectedEOF
				}
			}
			if err != nil {
				yield(nil, fmt.Errorf("source %s: %w", source.ID, err))
				return
			}
			coverage[i].Complete = true
		}
	}
	next, stop := iter.Pull2(seq)
	defer stop()
	inputs := &inputIter{next: next}
	query, err := gojq.Parse(req.Expression)
	if err != nil {
		return err
	}
	// Only this authorized iterator is exposed; no module or environment loaders.
	code, err := gojq.Compile(query, gojq.WithInputIter(inputs))
	if err != nil {
		return err
	}
	values := bytes.NewBufferString("[")
	result := code.RunWithContext(ctx, nil)
	for {
		value, ok := result.Next()
		if !ok {
			break
		}
		if err, ok := value.(error); ok {
			return err
		}
		body, err := gojq.Marshal(value)
		if err != nil {
			return err
		}
		if values.Len()+len(body)+1 > MaxOutputBytes {
			return ErrOutputLimit
		}
		if values.Len() > 1 {
			values.WriteByte(',')
		}
		values.Write(body)
	}
	if inputs.err != nil {
		return inputs.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	values.WriteByte(']')
	body, err := json.Marshal(output{Values: values.Bytes(), Coverage: coverage})
	if err != nil {
		return err
	}
	if len(body)+1 > MaxOutputBytes {
		return ErrOutputLimit
	}
	body = append(body, '\n')
	n, err := out.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	return err
}
