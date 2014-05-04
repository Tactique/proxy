package warserver

import (
	"encoding/json"
	"github.com/Tactique/golib/connection"
	"github.com/Tactique/golib/logger"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

const (
	RECV_BUF_LEN   = 1024
)

type websocketHandler interface {
	handleWebsocket(message []byte, cconn *clientConnection)
}

type proxyHandler func(data []byte, cconn *clientConnection, proxy *proxy)

type clientInfo struct {
	id    int
	Token string
}

type initialGameInfo struct {
	Uids  []int `json:"uids"`
	Debug int   `json:"debug"`
}

type killClient struct {
	Id int
}

type clientConnection struct {
	conn           connection.Connection
	currentHandler websocketHandler
	handlers       chan websocketHandler
	toClient       chan []byte
	info           clientInfo
}

type serverConnection struct {
	conn connection.Connection
}

type pipe struct {
	wsRecv   chan []byte
	sockRecv chan []byte
}

type proxy struct {
	proxyConns    []*clientConnection
	server        *serverConnection
	localHandlers map[string]proxyHandler
}

type ChatPacket struct {
	Message string `json:"message"`
}

func chatHandler(data []byte, cconn *clientConnection, proxy *proxy) {
	var request ChatPacket
	err := json.Unmarshal(data, &request)
	if err != nil {
		logger.Errorf("Malformed chat json: %s", data)
		return
	}
	resp := response{0, request}
	out := CommandMarshal("chat", resp)
	proxy.broadcast(out)
}

func newProxy(numPlayers int) *proxy {
	proxy := proxy{
		proxyConns:    make([]*clientConnection, numPlayers),
		localHandlers: make(map[string]proxyHandler),
	}
	proxy.registerLocalHandler("chat", chatHandler)
	return &proxy
}

func (p *proxy) registerLocalHandler(command string, handler proxyHandler) {
	p.localHandlers[command] = handler
}

func (p *proxy) slotClientConnection(slot int, cconn *clientConnection) {
	p.proxyConns[slot] = cconn
}

func (p *proxy) removeClientConnection(pos int) {
	j := pos + 1
	copy(p.proxyConns[pos:], p.proxyConns[j:])
	for k, n := len(p.proxyConns)-j+pos, len(p.proxyConns); k < n; k++ {
		p.proxyConns[k] = nil // or the zero value of T
	} // for k
	p.proxyConns = p.proxyConns[:len(p.proxyConns)-j+pos]
}

func (p *proxy) handleWebsocket(message []byte, cconn *clientConnection) {
	splitMsg := strings.SplitN(string(message), ":", 2)
	command := splitMsg[0]
	data := splitMsg[1]
	localHandler, ok := p.localHandlers[command]
	if ok {
		localHandler([]byte(data), cconn, p)
		return
	}
	logger.Infof("Proxying message from client: %s", message)
	message = appendClientInfo(command, data, cconn.info)
	err := p.server.conn.Write(message)
	if err != nil {
		logger.Errorf("Error while writing to socket: %s", err)
	}
}

func appendClientInfo(command string, data string, info clientInfo) []byte {
	// This will be disagreed with...
	clientIdStr := strconv.Itoa(info.id)
	return append([]byte(command), []byte(":"+clientIdStr+":"+data)...)
}

func (p *proxy) serverReadPump() {
	defer func() {
		p.server.conn.Close()
	}()
	for {
		msg, err := p.server.conn.Read()
		if err != nil {
			logger.Errorf("Error while reading from socket: %s", err)
			break
		}
		logger.Debugf("Received %s from socket", msg)
		p.broadcast(msg)
	}
}

func (p *proxy) broadcast(message []byte) {
    filteredMsg := filterClientInfo(message)
	for i := 0; i < len(p.proxyConns); i++ {
		p.proxyConns[i].toClient <- filteredMsg
	}
}

func filterClientInfo(message []byte) []byte {
	splitMsg := strings.SplitN(string(message), ":", 3)
	// if the message does not contain a client Id, it will only split once
	if len(splitMsg) <= 2 {
		return message
	}
    _, err := strconv.Atoi(splitMsg[1])
    // err == nil implies the command did not contain the client info
    if err != nil {
        return message
    }
	filteredMsg := append([]byte(splitMsg[0]), []byte(":"+splitMsg[2])...)
	return filteredMsg
}

func (p *proxy) sendInitialGameInfo() {
	// I'll send basically this once the server can accept it
	uids := make([]int, len(p.proxyConns))
	for i, v := range p.proxyConns {
		uids[i] = v.info.id
	}
	gi := initialGameInfo{uids, 0}
	message := CommandMarshal("new", gi)
	logger.Infof("%s", message)
	p.server.conn.Write([]byte(message))
}

func (pc *clientConnection) wsReadPump() {
	defer func() {
		pc.conn.Close()
	}()
	for {
		msg, err := pc.conn.Read()
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// the client ID here is redundant...
				kc := killClient{pc.info.id}
				killcconn := CommandMarshal("killClient", kc)
				pc.currentHandler.handleWebsocket([]byte(killcconn), pc)
			} else {
				logger.Errorf("Error while reading from websocket: %s", err)
			}
			break
		}
		logger.Debugf("Received %s from websocket", msg)
		select {
		case newHandler := <-pc.handlers:
			pc.currentHandler = newHandler
		default:
		}
		pc.currentHandler.handleWebsocket(msg, pc)
	}
}

func (pc *clientConnection) wsWritePump() {
	for msg := range pc.toClient {
		logger.Debugf("Writing %s to websocket", msg)
		err := pc.conn.Write(msg)
		if err != nil {
			logger.Errorf("Error while writing to websocket: %s", err)
			break
		}
	}
}

func connectToServer() (connection.Connection, error) {
	// weeeee, global variables
	conn, err := net.Dial("tcp", string(porterIP)+porterPort.Port)
	if err != nil {
		return nil, err
	}
	return connection.NewSocketConn(conn), nil
}

func serveWs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if strings.Split(r.Header.Get("Origin"), ":")[1] != strings.Split("http://"+r.Host, ":")[1] {
		logger.Warnf("Cross origin problem: %s", r.Host)
		http.Error(w, "Origin not allowed", 403)
		return
	}
	ws, err := websocket.Upgrade(w, r, nil, 1024, 1024)
	if _, ok := err.(websocket.HandshakeError); ok {
		http.Error(w, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		logger.Errorf("Websocket upgrade error: %s", err)
		return
	}
	ws.SetReadLimit(RECV_BUF_LEN)
	conn := NewWebsocketConn(ws)
	gamehub.connRegister <- conn
}

func socketListen(port string) {
	ln, err := net.Listen("tcp", port)
	if err != nil {
		logger.Errorf("Could not open socket for listening")
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			logger.Errorf("Could not accept connection from client")
			continue
		}
		sconn := connection.NewSocketConn(conn)
		gamehub.connRegister <- sconn
	}
}
