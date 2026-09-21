package httpapi_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func wsDial(t *testing.T, e *env, token string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WriteJSON(map[string]any{"t": "auth", "d": map[string]any{"token": token}})
	return c
}

func wsReady(t *testing.T, c *websocket.Conn) {
	t.Helper()
	for i := 0; i < 5; i++ {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var f struct{ T string }
		if err := c.ReadJSON(&f); err != nil {
			t.Fatalf("waiting for ready: %v", err)
		}
		if f.T == "ready" {
			return
		}
	}
	t.Fatal("never became ready")
}

func wsClosedWith(c *websocket.Conn) int {
	for i := 0; i < 20; i++ {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, _, err := c.ReadMessage(); err != nil {
			if ce, ok := err.(*websocket.CloseError); ok {
				return ce.Code
			}
			return -1
		}
	}
	return 0
}
