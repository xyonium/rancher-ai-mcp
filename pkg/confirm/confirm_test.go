package confirm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testOp() Operation {
	return Operation{Tool: "patchKubernetesResource", Cluster: "c-abc", Namespace: "default", Kind: "deployment", Name: "web", Payload: []byte(`[{"op":"replace"}]`)}
}

func TestTokenRoundTrip(t *testing.T) {
	g, err := NewGate()
	require.NoError(t, err)
	tok, err := g.IssueToken(testOp())
	require.NoError(t, err)
	require.NoError(t, g.RequireToken(testOp(), tok))
}

func TestTokenTampered(t *testing.T) {
	g, _ := NewGate()
	tok, _ := g.IssueToken(testOp())
	err := g.RequireToken(testOp(), tok[:len(tok)-2]+"xx")
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestTokenMismatchPayload(t *testing.T) {
	g, _ := NewGate()
	tok, _ := g.IssueToken(testOp())
	op := testOp()
	op.Payload = []byte(`[{"op":"add"}]`)
	assert.ErrorIs(t, g.RequireToken(op, tok), ErrTokenMismatch)
	op = testOp()
	op.Name = "other"
	assert.ErrorIs(t, g.RequireToken(op, tok), ErrTokenMismatch)
}

func TestTokenExpired(t *testing.T) {
	g, _ := NewGate()
	tok, _ := g.IssueToken(testOp())
	g.nowFn = func() time.Time { return time.Now().Add(11 * time.Minute) }
	assert.ErrorIs(t, g.RequireToken(testOp(), tok), ErrTokenExpired)
}

func TestTokenSingleUse(t *testing.T) {
	g, _ := NewGate()
	tok, _ := g.IssueToken(testOp())
	require.NoError(t, g.RequireToken(testOp(), tok))
	assert.ErrorIs(t, g.RequireToken(testOp(), tok), ErrTokenConsumed)
}

func TestConfirmTypedName(t *testing.T) {
	g, _ := NewGate()
	g.ElicitFunc = func(_ context.Context, _ *mcp.ServerSession, p *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		assert.Contains(t, p.Message, "web")
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmName": "web"}}, nil
	}
	ok, err := g.Confirm(context.Background(), nil, "delete deployment web", "web")
	require.NoError(t, err)
	assert.True(t, ok)

	g.ElicitFunc = func(_ context.Context, _ *mcp.ServerSession, _ *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmName": "WRONG"}}, nil
	}
	ok, err = g.Confirm(context.Background(), nil, "delete deployment web", "web")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestConfirmDeclineAndCancel(t *testing.T) {
	g, _ := NewGate()
	for _, action := range []string{"decline", "cancel"} {
		g.ElicitFunc = func(_ context.Context, _ *mcp.ServerSession, _ *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: action}, nil
		}
		ok, err := g.Confirm(context.Background(), nil, "patch x", "")
		require.NoError(t, err)
		assert.False(t, ok)
	}
}

func TestConfirmUnsupported(t *testing.T) {
	g, _ := NewGate()
	g.ElicitFunc = func(_ context.Context, _ *mcp.ServerSession, _ *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		return nil, errors.New("method not found")
	}
	_, err := g.Confirm(context.Background(), nil, "patch x", "")
	assert.ErrorIs(t, err, ErrConfirmationUnsupported)
	// default ElicitFunc with nil session must fail closed
	g2, _ := NewGate()
	_, err = g2.Confirm(context.Background(), nil, "patch x", "")
	assert.ErrorIs(t, err, ErrConfirmationUnsupported)
}

func TestCheckBypass(t *testing.T) {
	g, _ := NewGate()
	ok, err := g.Check(context.Background(), nil, testOp(), "", "summary", "", true)
	require.NoError(t, err)
	assert.True(t, ok)
}
