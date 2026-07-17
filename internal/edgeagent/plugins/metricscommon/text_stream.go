package metricscommon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/model"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	streamSampleChunkSize    = 1000
	maxPrometheusTextLineLen = 1 << 20
)

func streamTextSamples(ctx context.Context, r io.Reader, target Target, consume func([]tunnel.PromSample) error) (ScrapeStats, error) {
	var stats ScrapeStats
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxPrometheusTextLineLen)
	defaultTimestamp := time.Now().UnixMilli()
	chunk := make([]tunnel.PromSample, 0, streamSampleChunkSize)
	lineNumber := 0
	admitted := 0
	for scanner.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			return stats, fmt.Errorf("read exposition: %w", err)
		}
		sample, ok, err := parseTextSample(scanner.Bytes(), defaultTimestamp, target.ExtraLabels)
		if err != nil {
			return stats, fmt.Errorf("parse line %d: %w", lineNumber, err)
		}
		if !ok {
			continue
		}
		stats.Observed++
		if target.SampleLimit > 0 && admitted >= target.SampleLimit {
			stats.LimitExceeded = true
			break
		}
		chunk = append(chunk, sample)
		admitted++
		if len(chunk) == streamSampleChunkSize {
			applyLabelDrop(chunk, target.LabelDrop)
			if err := consume(chunk); err != nil {
				return stats, fmt.Errorf("consume samples: %w", err)
			}
			stats.Accepted += len(chunk)
			clear(chunk)
			chunk = chunk[:0]
		}
	}
	if err := scanner.Err(); err != nil {
		return stats, fmt.Errorf("read exposition: %w", err)
	}
	if len(chunk) > 0 {
		applyLabelDrop(chunk, target.LabelDrop)
		if err := consume(chunk); err != nil {
			return stats, fmt.Errorf("consume samples: %w", err)
		}
		stats.Accepted += len(chunk)
	}
	return stats, nil
}

func parseTextSample(line []byte, defaultTimestamp int64, extraLabels map[string]string) (tunnel.PromSample, bool, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] == '#' {
		return tunnel.PromSample{}, false, nil
	}

	nameEnd := 0
	for nameEnd < len(line) && line[nameEnd] != '{' && !isBlank(line[nameEnd]) {
		nameEnd++
	}
	if nameEnd == 0 {
		return tunnel.PromSample{}, false, fmt.Errorf("metric name required")
	}
	name := string(line[:nameEnd])
	if !model.IsValidMetricName(model.LabelValue(name)) {
		return tunnel.PromSample{}, false, fmt.Errorf("invalid metric name %q", name)
	}

	labels := make(map[string]string, len(extraLabels)+4)
	position := skipBlanks(line, nameEnd)
	if position < len(line) && line[position] == '{' {
		var err error
		position, err = parseTextLabels(line, position, labels)
		if err != nil {
			return tunnel.PromSample{}, false, err
		}
	}
	for key, value := range extraLabels {
		if _, exists := labels[key]; !exists {
			labels[key] = value
		}
	}
	position = skipBlanks(line, position)
	if position >= len(line) {
		return tunnel.PromSample{}, false, fmt.Errorf("value required for metric %q", name)
	}
	valueEnd := position
	for valueEnd < len(line) && !isBlank(line[valueEnd]) {
		valueEnd++
	}
	valueToken := string(line[position:valueEnd])
	value, err := strconv.ParseFloat(valueToken, 64)
	if err != nil {
		return tunnel.PromSample{}, false, fmt.Errorf("invalid value %q for metric %q: %w", valueToken, name, err)
	}

	timestamp := defaultTimestamp
	position = skipBlanks(line, valueEnd)
	if position < len(line) {
		timestampEnd := position
		for timestampEnd < len(line) && !isBlank(line[timestampEnd]) {
			timestampEnd++
		}
		timestampToken := string(line[position:timestampEnd])
		parsedTimestamp, parseErr := strconv.ParseInt(timestampToken, 10, 64)
		if parseErr != nil {
			return tunnel.PromSample{}, false, fmt.Errorf("invalid timestamp %q for metric %q: %w", timestampToken, name, parseErr)
		}
		if parsedTimestamp > 0 {
			timestamp = parsedTimestamp
		}
		if trailing := bytes.TrimSpace(line[timestampEnd:]); len(trailing) > 0 {
			return tunnel.PromSample{}, false, fmt.Errorf("unexpected trailing data %q", string(trailing))
		}
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return tunnel.PromSample{}, false, nil
	}
	if len(labels) == 0 {
		labels = nil
	}
	return tunnel.PromSample{Name: name, Labels: labels, Value: value, TsMs: timestamp}, true, nil
}

func parseTextLabels(line []byte, position int, labels map[string]string) (int, error) {
	position++
	for {
		position = skipBlanks(line, position)
		if position >= len(line) {
			return position, fmt.Errorf("unterminated label set")
		}
		if line[position] == '}' {
			return position + 1, nil
		}
		nameStart := position
		for position < len(line) && line[position] != '=' && !isBlank(line[position]) {
			position++
		}
		labelName := string(line[nameStart:position])
		if !model.LabelName(labelName).IsValid() || labelName == string(model.MetricNameLabel) {
			return position, fmt.Errorf("invalid label name %q", labelName)
		}
		if _, exists := labels[labelName]; exists {
			return position, fmt.Errorf("duplicate label name %q", labelName)
		}
		position = skipBlanks(line, position)
		if position >= len(line) || line[position] != '=' {
			return position, fmt.Errorf("expected '=' after label %q", labelName)
		}
		position = skipBlanks(line, position+1)
		if position >= len(line) || line[position] != '"' {
			return position, fmt.Errorf("expected quoted value for label %q", labelName)
		}
		value, next, err := parseTextLabelValue(line, position+1)
		if err != nil {
			return position, fmt.Errorf("label %q: %w", labelName, err)
		}
		if !model.LabelValue(value).IsValid() {
			return position, fmt.Errorf("invalid value for label %q", labelName)
		}
		labels[labelName] = value
		position = skipBlanks(line, next)
		if position >= len(line) {
			return position, fmt.Errorf("unterminated label set")
		}
		switch line[position] {
		case ',':
			position++
		case '}':
			return position + 1, nil
		default:
			return position, fmt.Errorf("expected ',' or '}' after label %q", labelName)
		}
	}
}

func parseTextLabelValue(line []byte, position int) (string, int, error) {
	var value strings.Builder
	for position < len(line) {
		switch line[position] {
		case '"':
			return value.String(), position + 1, nil
		case '\\':
			position++
			if position >= len(line) {
				return "", position, fmt.Errorf("unterminated escape sequence")
			}
			switch line[position] {
			case '\\', '"':
				value.WriteByte(line[position])
			case 'n':
				value.WriteByte('\n')
			default:
				return "", position, fmt.Errorf("invalid escape sequence \\%c", line[position])
			}
		default:
			value.WriteByte(line[position])
		}
		position++
	}
	return "", position, fmt.Errorf("unterminated quoted label value")
}

func skipBlanks(line []byte, position int) int {
	for position < len(line) && isBlank(line[position]) {
		position++
	}
	return position
}

func isBlank(value byte) bool {
	return value == ' ' || value == '\t'
}
