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
	"net/url"
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

func Bootstrap(bootstrapURL, fingerprint, proxyURL string, proxyEnabled bool) (string, error) {
	client := newHTTPClient(bootstrapTimeout, proxyURL, proxyEnabled)
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

// GenerateFingerprints generates n random hex-encoded fingerprints for
// multi-instance/load-balancing scenarios.
func GenerateFingerprints(n int) []string {
	fps := make([]string, n)
	b := make([]byte, 32)
	for i := range fps {
		cryptorand.Read(b)
		fps[i] = hex.EncodeToString(b)
	}
	return fps
}

// jwtPool manages a pool of JWT tokens, one per fingerprint, with
// round-robin selection and lazy per-entry refresh.
type JWTPool struct {
	mu           sync.Mutex
	entries      []JWTEntry
	counter      uint64
	proxyURL     string
	proxyEnabled bool
}

type JWTEntry struct {
	fingerprint string
	jwt         string
	exp         int64
}

func NewJWTPool(fingerprints []string, proxyURL string, proxyEnabled bool) *JWTPool {
	entries := make([]JWTEntry, len(fingerprints))
	for i, fp := range fingerprints {
		entries[i] = JWTEntry{fingerprint: fp}
	}
	return &JWTPool{
		entries:      entries,
		proxyURL:     proxyURL,
		proxyEnabled: proxyEnabled,
	}
}

// Select picks the next JWT in round-robin, refreshing it via bootstrap
// if expired or unset. Returns the JWT and its index (for invalidation).
func (p *JWTPool) Select(bootstrapURL string) (jwt string, idx int, err error) {
	p.mu.Lock()
	idx = int(p.counter % uint64(len(p.entries)))
	p.counter++
	entry := &p.entries[idx]
	if entry.jwt != "" && entry.exp-time.Now().UnixMilli() > jwtRefreshBuffer.Milliseconds() {
		jwt = entry.jwt
		p.mu.Unlock()
		return
	}
	fp := entry.fingerprint
	p.mu.Unlock()

	// Refresh this entry
	jwt, err = Bootstrap(bootstrapURL, fp, p.proxyURL, p.proxyEnabled)
	if err != nil {
		p.mu.Lock()
		if p.entries[idx].jwt != "" {
			jwt = p.entries[idx].jwt
			p.mu.Unlock()
			return jwt, idx, nil
		}
		p.mu.Unlock()
		return "", idx, err
	}

	p.mu.Lock()
	p.entries[idx].jwt = jwt
	p.entries[idx].exp = parseJWTExp(jwt)
	p.mu.Unlock()

	log.Printf("[JWT] Bootstrapped fingerprint[%d], exp in %v", idx, time.Until(time.UnixMilli(parseJWTExp(jwt))).Round(time.Second))
	return jwt, idx, nil
}

// Invalidate marks entry idx as expired so the next Select triggers a
// fresh bootstrap (used on 401/403 from upstream).
func (p *JWTPool) Invalidate(idx int) {
	p.mu.Lock()
	p.entries[idx].jwt = ""
	p.mu.Unlock()
}

// newHTTPClient creates an http.Client with optional proxy support.
// When a proxy is configured, keep-alives are disabled so each request
// opens a new TCP connection — needed for Clash-style load balancers
// to distribute across different exit nodes.
func newHTTPClient(timeout time.Duration, proxyURL string, proxyEnabled bool) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}

	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
		transport.DisableKeepAlives = true
	} else if proxyEnabled {
		transport.Proxy = http.ProxyFromEnvironment
		transport.DisableKeepAlives = true
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

type chatClient struct {
	httpClient *http.Client
	chatURL    string
}

func ProxyHandler(chatURL, bootstrapURL string, pool *JWTPool) http.HandlerFunc {
	cc := &chatClient{
		httpClient: newHTTPClient(0, pool.proxyURL, pool.proxyEnabled),
		chatURL:    chatURL,
	}

	return func(w http.ResponseWriter, r *http.Request) {
		jwt, idx, err := pool.Select(bootstrapURL)
		if err != nil {
			http.Error(w, `{"error":{"message":"JWT pool exhausted"}}`, http.StatusBadGateway)
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
			writeUpstreamError(w, r, err)
			return
		}

		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			resp.Body.Close()
			pool.Invalidate(idx)
			jwt, _, err = pool.Select(bootstrapURL)
			if err != nil {
				http.Error(w, `{"error":{"message":"JWT refresh failed"}}`, http.StatusBadGateway)
				return
			}
			resp, err = cc.doRequest(r.Context(), body, jwt)
			if err != nil {
				writeUpstreamError(w, r, err)
				return
			}
		}
		defer resp.Body.Close()

		// Upstream returned a non-200 error (rate-limit 429, 5xx, etc.). Surface
		// it to the client directly instead of trying to parse it as an SSE
		// stream — otherwise non-streaming clients get an empty body on 429.
		if resp.StatusCode != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			n, _ := io.Copy(w, io.LimitReader(resp.Body, maxResponseBody))
			log.Printf("[Proxy] Upstream returned %d (%d bytes forwarded)", resp.StatusCode, n)
			return
		}

		// The upstream always returns SSE. If the client asked for non-streaming,
		// aggregate the SSE chunks into a single JSON object.
		if clientStream {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(resp.StatusCode)

			flusher, canFlush := w.(http.Flusher)
			if canFlush {
				flusher.Flush()
			}

			n, err := io.Copy(w, resp.Body)
			if err != nil {
				if isExpectedStreamError(err) {
					// Client disconnected - log for diagnostics but not as error
					log.Printf("[Proxy] ⚠️  Client disconnected after %d bytes: %v", n, err)
				} else {
					log.Printf("[Proxy] ❌ Stream copy error after %d bytes: %v", n, err)
				}
			} else {
				log.Printf("[Proxy] ✅ Stream completed, %d bytes written", n)
			}

			// Always inject the SSE termination marker so clients receive
			// a clean end-of-stream signal. Without this, clients that rely
			// on [DONE] to transition from "streaming" to "completed" will
			// see a cancelled/incomplete request instead.
			written, writeErr := fmt.Fprintf(w, "data: [DONE]\n\n")
			if writeErr != nil {
				log.Printf("[Proxy] ⚠️  Failed to write [DONE] marker (%d bytes written): %v (client likely disconnected)", written, writeErr)
			} else {
				log.Printf("[Proxy] 🏁 [DONE] marker sent successfully")
			}
			if canFlush {
				flusher.Flush()
			}

			// Give the client a small buffer window (50ms) to read the [DONE] marker
			// before the connection is closed. This mitigates race conditions where
			// the TCP FIN arrives before the application layer processes [DONE].
			time.Sleep(50 * time.Millisecond)
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

	// Pre-encode to a buffer so we can set Content-Length for better client compatibility
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(resp); err != nil {
		log.Printf("[Proxy] Failed to encode non-stream response: %v", err)
		http.Error(w, `{"error":{"message":"Failed to encode response"}}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	w.WriteHeader(statusCode)
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("[Proxy] Failed to write non-stream response: %v", err)
	} else {
		log.Printf("[Proxy] ✅ Non-stream response sent (%d bytes)", buf.Len())
	}
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

// writeUpstreamError maps an upstream-request failure to a client-facing
// response. A client disconnect is silent; a timeout/rate-limit is a clear 502.
func writeUpstreamError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		// Client disconnected before the upstream responded — nothing to send.
		return
	}
	log.Printf("[Proxy] Upstream request failed: %v", err)
	msg := "Upstream request failed — the upstream may be rate-limiting or temporarily unavailable"
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "Upstream did not respond in time (rate-limited or unreachable)"
	}
	http.Error(w, fmt.Sprintf(`{"error":{"message":%q,"type":"upstream_error"}}`, msg), http.StatusBadGateway)
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