package warserver

import (
    "github.com/gorilla/websocket"
)

type WebsocketConn struct {
    ws *websocket.Conn
}

func NewWebsocketConn(ws *websocket.Conn) *WebsocketConn {
    return &WebsocketConn{ws: ws}
}

func (c *WebsocketConn) Read() ([]byte, error) {
    _, msg, err := c.ws.ReadMessage()
    return msg, err
}

func (c *WebsocketConn) Write(msg []byte) error {
    return c.ws.WriteMessage(websocket.TextMessage, msg)
}

func (c *WebsocketConn) Close() error {
    return c.ws.Close()
}

