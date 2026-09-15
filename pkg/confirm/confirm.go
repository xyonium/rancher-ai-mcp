// Package confirm implements the server-side safety gate for mutating tools:
// single-use HMAC confirmation tokens binding an exact operation, and a
// server-initiated user confirmation via MCP elicitation.
package confirm

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

var (
	ErrTokenInvalid  = errors.New("invalid confirmation token")
	ErrTokenExpired  = errors.New("confirmation token expired, call the Plan tool again")
	ErrTokenConsumed = errors.New("confirmation token already used, call the Plan tool again")
	ErrTokenMismatch = errors.New("confirmation token does not match this operation, call the Plan tool again with these exact parameters")

	// ErrConfirmationUnsupported means the connected client cannot answer a
	// server-initiated user confirmation. The operation must NOT be executed.
	ErrConfirmationUnsupported = errors.New("the MCP client does not support interactive user confirmation (elicitation); refusing to execute the mutating operation")
)

// Operation uniquely identifies one exact mutating call a user must approve.
type Operation struct {
	Tool      string
	Cluster   string
	Namespace string
	Kind      string
	Name      string
	Payload   []byte // canonical payload (manifest JSON, patch JSON, command bytes); nil for delete
}

type tokenBody struct {
	Tool        string `json:"tool"`
	Cluster     string `json:"cluster"`
	Namespace   string `json:"namespace,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Name        string `json:"name,omitempty"`
	PayloadHash string `json:"payloadHash,omitempty"`
	Nonce       string `json:"nonce"`
	Expiry      int64  `json:"expiry"`
}

// Gate issues and validates confirmation tokens and asks the user for
// explicit confirmation through the MCP client.
//
// A Gate must be constructed with NewGate: the zero value is not usable and
// panics on first use (nil nowFn and nil consumed map, and an all-zero HMAC
// key would sign every token with a predictable key).
type Gate struct {
	key      [32]byte
	mu       sync.Mutex
	consumed map[string]int64 // nonce -> expiry unix
	nowFn    func() time.Time

	// TokenTTL is how long an issued token stays valid. Default 10 minutes.
	TokenTTL time.Duration

	// ElicitFunc performs the actual elicitation. Replaceable in tests.
	// The default fails closed when the session is nil.
	ElicitFunc func(ctx context.Context, ss *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error)
}

// NewGate creates a Gate with a fresh random process-local HMAC key.
func NewGate() (*Gate, error) {
	g := &Gate{
		consumed: map[string]int64{},
		nowFn:    time.Now,
		TokenTTL: 10 * time.Minute,
	}
	if _, err := rand.Read(g.key[:]); err != nil {
		return nil, fmt.Errorf("generating confirmation key: %w", err)
	}
	g.ElicitFunc = func(ctx context.Context, ss *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		if ss == nil {
			return nil, errors.New("no MCP session available")
		}
		return ss.Elicit(ctx, params)
	}
	return g, nil
}

func payloadHash(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// IssueToken returns a single-use token binding the exact operation.
func (g *Gate) IssueToken(op Operation) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	body := tokenBody{
		Tool: op.Tool, Cluster: op.Cluster, Namespace: op.Namespace,
		Kind: op.Kind, Name: op.Name, PayloadHash: payloadHash(op.Payload),
		Nonce:  hex.EncodeToString(nonce),
		Expiry: g.nowFn().Add(g.TokenTTL).Unix(),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling token: %w", err)
	}
	mac := hmac.New(sha256.New, g.key[:])
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// RequireToken validates the token against the exact operation and consumes
// it. A token is consumed even if the caller later aborts, forcing a fresh
// plan (and fresh user approval) for every execution.
func (g *Gate) RequireToken(op Operation, token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return fmt.Errorf("%w: malformed token", ErrTokenInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	mac := hmac.New(sha256.New, g.key[:])
	mac.Write(raw)
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return fmt.Errorf("%w: bad signature", ErrTokenInvalid)
	}
	var body tokenBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	if g.nowFn().Unix() > body.Expiry {
		return ErrTokenExpired
	}
	if body.Tool != op.Tool || body.Cluster != op.Cluster || body.Namespace != op.Namespace ||
		body.Kind != op.Kind || body.Name != op.Name || body.PayloadHash != payloadHash(op.Payload) {
		return ErrTokenMismatch
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, used := g.consumed[body.Nonce]; used {
		return ErrTokenConsumed
	}
	g.consumed[body.Nonce] = body.Expiry
	// opportunistic sweep of expired nonces
	now := g.nowFn().Unix()
	for n, exp := range g.consumed {
		if exp < now {
			delete(g.consumed, n)
		}
	}
	return nil
}

// Confirm asks the USER, through the MCP client, to approve the operation.
// typedName != "" requires the user to type that exact string (used for
// deletions). Returns (false, nil) when the user declines or cancels.
func (g *Gate) Confirm(ctx context.Context, ss *mcp.ServerSession, summary, typedName string) (bool, error) {
	var schema map[string]any
	if typedName != "" {
		schema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirmName": map[string]any{
					"type":        "string",
					"description": fmt.Sprintf("Type the exact resource name %q to confirm this deletion", typedName),
				},
			},
			"required": []string{"confirmName"},
		}
	} else {
		schema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirm": map[string]any{
					"type":        "string",
					"enum":        []string{"approve", "reject"},
					"description": "Select approve to execute the operation exactly as shown, or reject to cancel it",
				},
			},
			"required": []string{"confirm"},
		}
	}
	res, err := g.ElicitFunc(ctx, ss, &mcp.ElicitParams{Message: summary, RequestedSchema: schema})
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrConfirmationUnsupported, err)
	}
	if res == nil || res.Action != "accept" {
		return false, nil
	}
	if typedName != "" {
		name, _ := res.Content["confirmName"].(string)
		return name == typedName, nil
	}
	confirm, _ := res.Content["confirm"].(string)
	return confirm == "approve", nil
}

// Check runs the full gate for a mutating call: token validation followed by
// direct user confirmation. bypass is true only for create/update-class tools
// when the operator explicitly started the server with --allow-auto-write;
// delete and exec tools always pass bypass=false.
func (g *Gate) Check(ctx context.Context, ss *mcp.ServerSession, op Operation, token, summary, typedName string, bypass bool) (bool, error) {
	fields := []zap.Field{
		zap.String("tool", op.Tool), zap.String("cluster", op.Cluster),
		zap.String("namespace", op.Namespace), zap.String("kind", op.Kind), zap.String("name", op.Name),
	}
	if bypass {
		zap.L().Info("write operation executing WITHOUT user confirmation (auto-write mode)", fields...)
		return true, nil
	}
	if err := g.RequireToken(op, token); err != nil {
		zap.L().Info("write operation rejected by token gate", append(fields, zap.String("reason", err.Error()))...)
		return false, err
	}
	approved, err := g.Confirm(ctx, ss, summary, typedName)
	if err != nil {
		zap.L().Info("write operation rejected: user confirmation unavailable", append(fields, zap.String("reason", err.Error()))...)
		return false, err
	}
	zap.L().Info("write operation user decision", append(fields, zap.Bool("approved", approved))...)
	return approved, nil
}

// CancelledResult is the standard tool result when the user declines or
// cancels the confirmation: a normal result so the agent reports it cleanly.
func CancelledResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Operation cancelled by the user. Nothing was executed."}},
	}
}
