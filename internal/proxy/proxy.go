package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	jwtRefreshBuffer = 5 * time.Minute
	bootstrapTimeout = 15 * time.Second
	maxBodySize      = 1 << 20 // 1MB
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
	seed := fmt.Sprintf("%s|%s|%s|%s|%s", hostname, runtime.GOOS, runtime.GOARCH, cpu, username)
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
	body, _ := json.Marshal(map[string]string{"client": fingerprint})
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
			Timeout: 300 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        50,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
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
		body := rewriteModelField(rawBody)

		resp, err := cc.doRequest(r, body, jwt)
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
			resp, err = cc.doRequest(r, body, jwt)
			if err != nil {
				http.Error(w, `{"error":{"message":"Upstream error"}}`, http.StatusBadGateway)
				return
			}
		}
		defer resp.Body.Close()

		for key, values := range resp.Header {
			for _, v := range values {
				w.Header().Add(key, v)
			}
		}

		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			w.WriteHeader(resp.StatusCode)
			if _, err := io.Copy(w, resp.Body); err != nil {
				log.Printf("[Proxy] Stream copy error: %v", err)
			}
		} else {
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Printf("[Proxy] Failed to read non-stream body: %v", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte(`{"error":{"message":"Failed to read upstream response"}}`))
				return
			}
			bodyStr := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(respBody)), "data:"))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write([]byte(bodyStr))
		}
	}
}

func (cc *chatClient) doRequest(r *http.Request, body []byte, jwt string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), "POST", cc.chatURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-Mimo-Source", "mimocode-cli-free")
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("User-Agent", randomUA())
	return cc.httpClient.Do(req)
}

var uaList = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.2 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
}

func randomUA() string {
	return uaList[rand.Intn(len(uaList))]
}

// chatRequest is a minimal struct for parsing only the model field.
type chatRequest struct {
	Model string `json:"model"`
}

func rewriteModelField(body []byte) []byte {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	if idx := strings.LastIndex(req.Model, "/"); idx >= 0 {
		req.Model = req.Model[idx+1:]
		// Marshal just the model field back into the original JSON.
		// Use a map to preserve all fields.
		var full map[string]json.RawMessage
		if err := json.Unmarshal(body, &full); err != nil {
			return body
		}
		full["model"] = json.RawMessage(`"` + req.Model + `"`)
		result, err := json.Marshal(full)
		if err != nil {
			return body
		}
		return result
	}
	return body
}