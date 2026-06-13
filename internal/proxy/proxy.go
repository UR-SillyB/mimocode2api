package proxy

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	jwtRefreshBuffer   = 5 * time.Minute
	bootstrapTimeout   = 15 * time.Second
	maxBodySize        = 32 << 20 // 32MB — vision inputs (base64 images) easily exceed 1MB
	maxResponseBody    = 5 << 20 // 5MB
)

type bootstrapResponse struct {
	JWT string `json:"jwt"`
}

type jwtCache struct {
	mu  sync.Mutex
	jwt string
	exp int64
}

var cache jwtCache

func nodePlatform(goos string) string {
	switch goos {
	case "windows":
		return "win32"
	default:
		return goos // darwin → darwin, linux → linux
	}
}

func nodeArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	default:
		return goarch // arm64 → arm64
	}
}

func GenerateFingerprint() string {
	hostname, _ := os.Hostname()
	cpu := detectCPU()
	username := "unknown-user"
	if u, err := os.UserHomeDir(); err == nil {
		parts := strings.Split(u, "/")
		if len(parts) > 0 {
			username = parts[len(parts)-1]
		}
	}
	seed := fmt.Sprintf("%s|%s|%s|%s|%s", hostname, nodePlatform(runtime.GOOS), nodeArch(runtime.GOARCH), cpu, username)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(seed)))
}

func detectCPU() string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	case "linux":
		data, err := os.ReadFile("/proc/cpuinfo")
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "model name") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						return strings.TrimSpace(parts[1])
					}
				}
			}
		}
	}
	return "unknown-cpu"
}

func parseJWTExp(jwt string) int64 {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return time.Now().Add(50 * time.Minute).UnixMilli()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Now().Add(50 * time.Minute).UnixMilli()
	}
	var claims struct{ Exp int64 `json:"exp"` }
	if json.Unmarshal(payload, &claims) != nil {
		return time.Now().Add(50 * time.Minute).UnixMilli()
	}
	return claims.Exp * 1000
}

func Bootstrap(bootstrapURL, fingerprint string) (string, error) {
	client := &http.Client{Timeout: bootstrapTimeout}
	body, err := json.Marshal(map[string]string{"client": fingerprint})
	if err != nil {
		return "", fmt.Errorf("bootstrap marshal: %w", err)
	}
	resp, err := client.Post(bootstrapURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("bootstrap: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return "", fmt.Errorf("bootstrap: %d %s", resp.StatusCode, string(b))
	}

	var result bootstrapResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("bootstrap decode: %w", err)
	}
	if result.JWT == "" {
		return "", fmt.Errorf("bootstrap: no jwt in response")
	}
	return result.JWT, nil
}

func GetJWT(bootstrapURL, fingerprint string) (string, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.jwt != "" && cache.exp-time.Now().UnixMilli() > jwtRefreshBuffer.Milliseconds() {
		return cache.jwt, nil
	}

	jwt, err := Bootstrap(bootstrapURL, fingerprint)
	if err != nil {
		if cache.jwt != "" {
			log.Printf("[JWT] Bootstrap failed, using cached: %v", err)
			return cache.jwt, nil
		}
		return "", err
	}

	cache.jwt = jwt
	cache.exp = parseJWTExp(jwt)
	log.Printf("[JWT] Bootstrapped, exp in %v", time.Until(time.UnixMilli(cache.exp)).Round(time.Second))
	return jwt, nil
}

func invalidateJWT() {
	cache.mu.Lock()
	cache.jwt = ""
	cache.mu.Unlock()
}

type chatClient struct {
	httpClient *http.Client
	chatURL    string
}

func ProxyHandler(chatURL, bootstrapURL, fingerprint string) http.HandlerFunc {
	cc := &chatClient{
		httpClient: &http.Client{
			// Timeout 设为 0：SSE 流式响应可能持续数分钟，全局超时会中途掐断
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConns:          50,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
		chatURL: chatURL,
	}

	return func(w http.ResponseWriter, r *http.Request) {
		jwt, err := GetJWT(bootstrapURL, fingerprint)
		if err != nil {
			http.Error(w, `{"error":{"message":"JWT bootstrap failed"}}`, http.StatusBadGateway)
			return
		}

		rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
		r.Body.Close()
		if err != nil {
			http.Error(w, `{"error":{"message":"Failed to read body"}}`, http.StatusBadRequest)
			return
		}

		// Detect the client's original stream preference before normalizing
		clientStream := true
		var rawReq struct {
			Stream *bool `json:"stream"`
		}
		if json.Unmarshal(rawBody, &rawReq) == nil && rawReq.Stream != nil {
			clientStream = *rawReq.Stream
		}

		body := normalizeBody(rawBody)

		resp, err := cc.doRequest(r.Context(), body, jwt)
		if err != nil {
			http.Error(w, `{"error":{"message":"Upstream error"}}`, http.StatusBadGateway)
			return
		}

		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			resp.Body.Close()
			invalidateJWT()
			jwt, err = GetJWT(bootstrapURL, fingerprint)
			if err != nil {
				http.Error(w, `{"error":{"message":"JWT refresh failed"}}`, http.StatusBadGateway)
				return
			}
			resp, err = cc.doRequest(r.Context(), body, jwt)
			if err != nil {
				http.Error(w, `{"error":{"message":"Upstream error"}}`, http.StatusBadGateway)
				return
			}
		}
		defer resp.Body.Close()

		// The upstream always returns SSE. If the client asked for non-streaming,
		// aggregate the SSE chunks into a single JSON object.
		if clientStream {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(resp.StatusCode)

			n, err := io.Copy(w, resp.Body)
			if err != nil && !isExpectedStreamError(err) {
				log.Printf("[Proxy] Stream copy error after %d bytes: %v", n, err)
			} else if err == nil {
				log.Printf("[Proxy] Stream completed, %d bytes written", n)
			}
			// If isExpectedStreamError, client disconnected — normal, don't log

			// Ensure all buffered bytes are flushed to the client before the
			// handler returns — otherwise the last SSE chunk may be delayed.
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		} else {
			aggregateSSE(w, resp.Body, resp.StatusCode)
		}
	}
}

// aggregateSSE reads an SSE stream line by line and builds a single
// non-streaming chat completion JSON response.
func aggregateSSE(w http.ResponseWriter, body io.Reader, statusCode int) {
	var (
		content       strings.Builder
		reasoning     strings.Builder
		completionID  string
		created       int64
		modelName     string
		role          = "assistant"
		finishReason  string
		usage         json.RawMessage
	)

	scanner := NewSSEScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "[DONE]" || payload == "" {
			continue
		}

		var chunk struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
					Reason  string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		if chunk.ID != "" {
			completionID = chunk.ID
		}
		if chunk.Created > 0 {
			created = chunk.Created
		}
		if chunk.Model != "" {
			modelName = chunk.Model
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Role != "" {
				role = choice.Delta.Role
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
			}
			if choice.Delta.Reason != "" {
				reasoning.WriteString(choice.Delta.Reason)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}

	resp := map[string]any{
		"id":      completionID,
		"object":  "chat.completion",
		"created": created,
		"model":   modelName,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    role,
					"content": content.String(),
				},
				"finish_reason": finishReason,
			},
		},
	}
	if reasoning.Len() > 0 {
		msg := resp["choices"].([]map[string]any)[0]["message"].(map[string]any)
		msg["reasoning_content"] = reasoning.String()
	}
	if usage != nil {
		resp["usage"] = json.RawMessage(usage)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}

// SSEScanner wraps a bufio.Scanner for reading SSE lines.
type SSEScanner struct {
	*bufio.Scanner
}

func NewSSEScanner(r io.Reader) *SSEScanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxResponseBody)
	return &SSEScanner{Scanner: s}
}

// isExpectedStreamError returns true if the error is an expected stream
// termination (client disconnect, context canceled, broken pipe).
// These are normal when clients close connections and shouldn't be logged as errors.
func isExpectedStreamError(err error) bool {
	if err == nil || err == io.EOF {
		return true
	}
	// Client disconnected or context canceled
	if errors.Is(err, context.Canceled) {
		return true
	}
	// Check for common "broken pipe" errors (client closed connection)
	errStr := err.Error()
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "use of closed network connection")
}

func generateSessionAffinity() string {
	b := make([]byte, 12)
	cryptorand.Read(b)
	return "ses_" + hex.EncodeToString(b)
}

func (cc *chatClient) doRequest(ctx context.Context, body []byte, jwt string) (*http.Response, error) {
	// Bind the upstream request to the client's request context so that when
	// the client disconnects, reading from resp.Body returns promptly and the
	// upstream connection is torn down instead of lingering until a write fails.
	// The caller is responsible for reading and closing resp.Body.
	req, err := http.NewRequestWithContext(ctx, "POST", cc.chatURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-Mimo-Source", "mimocode-cli-free")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "mimocode/0.1.0 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14")
	req.Header.Set("x-session-affinity", generateSessionAffinity())
	return cc.httpClient.Do(req)
}

// Magic system prompt prefix required by the MiMo upstream.
// Without it the upstream returns 403.
const mimoMagicPrefix = "# Memory system\n\nYou have a persistent file-based memory system. Four file types"

// chatRequest is a minimal struct for parsing only the model field.
type chatRequest struct {
	Model string `json:"model"`
}

func normalizeBody(body []byte) []byte {
	var full map[string]json.RawMessage
	if err := json.Unmarshal(body, &full); err != nil {
		return body
	}

	// Rewrite model: strip provider prefix, map aliases to mimo-auto
	if rawModel, ok := full["model"]; ok {
		var modelStr string
		if err := json.Unmarshal(rawModel, &modelStr); err == nil {
			if idx := strings.LastIndex(modelStr, "/"); idx >= 0 {
				modelStr = modelStr[idx+1:]
			}
			// All models route to mimo-auto on the free upstream endpoint
			modelStr = "mimo-auto"
			full["model"] = json.RawMessage(`"` + modelStr + `"`)
		}
	}

	// Ensure stream is always true (upstream returns SSE)
	full["stream"] = json.RawMessage(`true`)
	full["stream_options"] = json.RawMessage(`{"include_usage":true}`)

	// Insert the magic system prompt prefix
	var messages []json.RawMessage
	if rawMsgs, ok := full["messages"]; ok {
		json.Unmarshal(rawMsgs, &messages)
	}

	if len(messages) > 0 {
		var firstMsg map[string]json.RawMessage
		if err := json.Unmarshal(messages[0], &firstMsg); err == nil {
			if role, ok := firstMsg["role"]; ok {
				var roleStr string
				if json.Unmarshal(role, &roleStr) == nil && roleStr == "system" {
					if content, ok := firstMsg["content"]; ok {
						// Try to parse as string first (simple text content)
						var contentStr string
						if json.Unmarshal(content, &contentStr) == nil {
							if !strings.HasPrefix(contentStr, mimoMagicPrefix) {
								firstMsg["content"], _ = json.Marshal(mimoMagicPrefix + "\n\n" + contentStr)
								messages[0], _ = json.Marshal(firstMsg)
							}
						}
						// If content is an array (multimodal with images), leave it unchanged
						// The magic prefix is already injected via the prepended system message below
					}
				} else {
					// First message is not system — prepend a system message
					messages = append([]json.RawMessage{
						json.RawMessage(fmt.Sprintf(`{"role":"system","content":%s}`, mustMarshal(mimoMagicPrefix))),
					}, messages...)
				}
			}
		}
	} else {
		// No messages at all — inject a system message
		magicMsg := fmt.Sprintf(`{"role":"system","content":%s}`, mustMarshal(mimoMagicPrefix))
		messages = append([]json.RawMessage{json.RawMessage(magicMsg)}, messages...)
	}

	full["messages"], _ = json.Marshal(messages)
	result, _ := json.Marshal(full)
	return result
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
}