package unihub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

type proCounts struct{ input, output, hit, miss int }

var errProEvidence = errors.New("incomplete or inconsistent native usage")

const maxNativeBody = 8 << 20

func boundedNativeBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxNativeBody+1))
	if len(b) > maxNativeBody {
		return nil, errProEvidence
	}
	return b, err
}

// Return zero counts for a content-only stream event. Never infer prompt or
// completion counts from text. The cache buckets partition total prompt tokens.
func readProFrame(payload []byte, chat proChat) (proCounts, error) {
	var frame struct {
		Model string          `json:"model"`
		Error json.RawMessage `json:"error"`
		Usage *struct {
			Input   *int `json:"prompt_tokens"`
			Output  *int `json:"completion_tokens"`
			Total   *int `json:"total_tokens"`
			Hit     *int `json:"prompt_cache_hit_tokens"`
			Miss    *int `json:"prompt_cache_miss_tokens"`
			Details struct {
				Cached *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			Billing struct {
				Estimated bool `json:"estimated"`
			} `json:"billing_usage"`
			// rc.51 re-serializes a full zero-filled dto.Usage on its local
			// estimate fallback. A normalized-only reply of that shape cannot
			// prove that the upstream actually reported cached_tokens=0.
			InputDetails json.RawMessage `json:"input_tokens_details"`
			InputTokens  *int            `json:"input_tokens"`
			OutputTokens *int            `json:"output_tokens"`
		} `json:"usage"`
	}
	var zero proCounts
	if common.Unmarshal(payload, &frame) != nil || frame.Model != deepSeekPro || (len(frame.Error) > 0 && string(frame.Error) != "null") {
		return zero, errProEvidence
	}
	u := frame.Usage
	if u == nil {
		return zero, nil
	}
	if u.Billing.Estimated || u.Input == nil || u.Output == nil || *u.Input < 1 || *u.Input > chat.inputBound || *u.Output < 0 || *u.Output > chat.maxTokens {
		return zero, errProEvidence
	}
	if u.Total != nil && *u.Total != *u.Input+*u.Output {
		return zero, errProEvidence
	}
	hit := u.Hit
	if cached := u.Details.Cached; cached != nil {
		if hit != nil && *hit != *cached {
			return zero, errProEvidence
		}
		if hit == nil && u.Miss == nil && *cached == 0 && string(u.InputDetails) == "null" && u.InputTokens != nil && *u.InputTokens == 0 &&
			u.OutputTokens != nil && *u.OutputTokens == 0 {
			return zero, errProEvidence
		}
		hit = cached
	}
	count := proCounts{input: *u.Input, output: *u.Output}
	switch {
	case hit != nil:
		if *hit < 0 || *hit > count.input {
			return zero, errProEvidence
		}
		count.hit = *hit
	case u.Miss != nil:
		if *u.Miss < 0 || *u.Miss > count.input {
			return zero, errProEvidence
		}
		count.hit = count.input - *u.Miss
	default:
		return zero, errProEvidence
	}
	count.miss = count.input - count.hit
	if u.Miss != nil && *u.Miss != count.miss {
		return zero, errProEvidence
	}
	return count, nil
}

// Forward complete native SSE events as they arrive. Only the terminal [DONE]
// is deferred until a confirmed settlement. A partial frame/EOF is ambiguous.
func readProStream(c *gin.Context, r io.Reader, chat proChat) (proCounts, error) {
	var usage proCounts
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 32<<10), maxNativeBody)
	var event, data bytes.Buffer
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Header().Add("Trailer", "X-UniHub-Settled-Units")
	for scanner.Scan() {
		line := scanner.Text()
		if event.Len()+len(line)+1 > maxNativeBody {
			return usage, errProEvidence
		}
		event.WriteString(line)
		event.WriteByte('\n')
		if line != "" {
			if v, ok := strings.CutPrefix(line, "data:"); ok {
				data.WriteString(strings.TrimPrefix(v, " "))
				data.WriteByte('\n')
			} else if !strings.HasPrefix(line, ":") {
				return usage, errProEvidence
			}
			continue
		}
		payload := bytes.TrimSpace(data.Bytes())
		if bytes.Equal(payload, []byte("[DONE]")) {
			if usage.input == 0 {
				return usage, errProEvidence
			}
			return usage, nil
		}
		if len(payload) > 0 {
			count, err := readProFrame(payload, chat)
			if err != nil {
				return usage, err
			}
			if count.input > 0 {
				if usage.input > 0 && count != usage {
					return usage, errProEvidence
				}
				usage = count
			}
		}
		if _, err := c.Writer.Write(event.Bytes()); err != nil {
			return usage, err
		}
		c.Writer.Flush()
		event.Reset()
		data.Reset()
	}
	return usage, errProEvidence
}
