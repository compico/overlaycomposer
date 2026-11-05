package twitch

import (
	"context"
	"time"

	"github.com/fasthttp/websocket"
)

const dialTimeout = 10 * time.Second

type wsAdapter struct{ *websocket.Conn }

func (a wsAdapter) ReadMessage() ([]byte, error) {
	_, data, err := a.Conn.ReadMessage()
	return data, err
}

// dialWS — реальное WebSocket-соединение (fasthttp/websocket, как и у клиента OBS).
func dialWS(ctx context.Context, url string) (wsConn, error) {
	d := websocket.Dialer{HandshakeTimeout: dialTimeout}
	c, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return wsAdapter{c}, nil
}
