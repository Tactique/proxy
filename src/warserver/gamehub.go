package warserver

import (
	"container/list"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Tactique/golib/connection"
	"github.com/Tactique/golib/logger"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"strings"
)

const (
	NUM_PLAYERS = 2
)

type newGame struct {
	NumPlayers int
	cconn      *clientConnection
}

type localHandler func(message string, cconn *clientConnection)

type game_hub struct {
	gameRequests     chan *newGame
	uncommittedGames map[int]*game
	committedGames   *list.List
	connRegister     chan connection.Connection
	localHandlers    map[string]localHandler
}

type response struct {
	Status  int         `json:"status"`
	Payload interface{} `json:"payload"`
}

func CommandMarshal(cmd string, payload interface{}) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Warnf("Error marshalling json: ", err)
		return nil
	}
	return append([]byte(cmd+":"), body...)
}

func (gh *game_hub) handleWebsocket(message []byte, cconn *clientConnection) {
	cmds := strings.SplitN(string(message), ":", 2)
	if len(cmds) == 2 {
		if fun, ok := gh.localHandlers[cmds[0]]; ok {
			fun(cmds[1], cconn)
		} else {
			logger.Warnf("Unrecognized command: %s", cmds[0])
			cconn.toClient <- []byte("unrecognized:")
		}
	} else {
		logger.Errorf("Malformed command: %s", cmds)
	}
}

func (gh *game_hub) handleClientInfo(message string, cconn *clientConnection) {
	ci := clientInfo{}
	resp := response{0, nil}
	// I hate repeating this unmarshalling code, does Go allow something more general?
	err := json.Unmarshal([]byte(message), &ci)
	if err != nil {
		logger.Warnf("Error unmarshalling json: %s", err)
		return
	}
	userid, err := getClientIdFromToken(ci.Token)
	if err != nil {
		logger.Errorf("Error querying database: %s", err)
		resp.Status = -1
		cconn.toClient <- CommandMarshal("clientInfo", resp)
		return
	}
	ci.id = userid
	cconn.info = ci
	cconn.toClient <- CommandMarshal("clientInfo", resp)
}

func getClientIdFromToken(token string) (int, error) {
	dbPath := os.Getenv("ROOTIQUE") + "/common/database/db.sqlite3"
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		logger.Fatalf("%s", err)
	}
	defer db.Close()

	sql := fmt.Sprintf("select id, userid from interface_logindata where token='%s';", token)
	rows, err := db.Query(sql)
	if err != nil {
		return 0, err
	}
	var rowId int
	var userId int
	if rows.Next() {
		err := rows.Scan(&rowId, &userId)
		if err != nil {
			return 0, err
		}
	}
	if rows.Next() {
		return 0, errors.New("More than one entry returned in user query")
	}
	rows.Close()
	deleteRow := fmt.Sprintf("delete from interface_logindata where id='%s';", rowId)
	_, err = db.Exec(deleteRow)
	if err != nil {
		return 0, err
	}

	return userId, nil
}

func (gh *game_hub) handleNewGame(message string, cconn *clientConnection) {
	ng := newGame{}
	err := json.Unmarshal([]byte(message), &ng)
	if err != nil {
		logger.Warnf("Error unmarshalling json: %s", err)
		return
	}
	ng.cconn = cconn
	logger.Infof("Got new game %s", ng)
	gh.gameRequests <- &ng
}

func (gh *game_hub) handleDisconnection(message string, cconn *clientConnection) {
	// Find the connection and kill it, cleaning up its game if necessary
	logger.Info("Client Disconnected. Cleaning up...")
	for np, game := range gh.uncommittedGames {
		for i := 0; i < game.currentPlayers; i++ {
			if game.proxy.proxyConns[i].info.id == cconn.info.id {
				game.proxy.removeClientConnection(i)
				game.currentPlayers -= 1
				if game.currentPlayers == 0 {
					logger.Infof("%d player uncommitted game empty. Dropping", np)
					delete(gh.uncommittedGames, np)
				}
				break
			}
		}
	}
}

func (gh *game_hub) handleConnections() {
	for conn := range gh.connRegister {
		cconn := clientConnection{conn: conn, currentHandler: gh,
			handlers: make(chan websocketHandler, 5),
			toClient: make(chan []byte)}
		go cconn.wsReadPump()
		go cconn.wsWritePump()
	}
}

func (gh *game_hub) makeGame(numPlayers int) *game {
	proxy := *newProxy(numPlayers)
	game := game{numPlayers: numPlayers, currentPlayers: 0,
		proxy: &proxy}
	gh.uncommittedGames[numPlayers] = &game

	return &game
}

func (gh *game_hub) commitGame(game *game) {
	delete(gh.uncommittedGames, game.numPlayers)
	// make connection to server

	conn, err := connectToServer()
	if err != nil {
		logger.Errorf("Could not connect to server, this game is going to hang...")
		return
	}
	game.proxy.server = &serverConnection{conn: conn}
	game.channelInHandler(game.proxy)
	go game.proxy.serverReadPump()
	game.proxy.sendInitialGameInfo()
	logger.Info("Committed a game, proxying its messages")

	gh.committedGames.PushBack(game)
}

func (gh *game_hub) processNewGameRequests() {
	for ng := range gh.gameRequests {
		// look for an existing game to satisfy the new request
		gm := gh.findGame(ng)
		// create a game if one can't be found
		if gm == nil {
			logger.Info("Couldn't find an available game. Creating a new one")
			gm = gh.makeGame(ng.NumPlayers)
		} else {
			logger.Info("Found existing game. Slotting in")
		}
		gm.proxy.slotClientConnection(gm.currentPlayers, ng.cconn)
		gm.currentPlayers += 1
		if gm.currentPlayers == gm.numPlayers {
			gh.commitGame(gm)
		}
	}
}

func (gh *game_hub) findGame(ng *newGame) *game {
	game := gh.uncommittedGames[ng.NumPlayers]
	return game
}

var gamehub = game_hub{
	gameRequests:     make(chan *newGame),
	uncommittedGames: make(map[int]*game),
	committedGames:   list.New(),
	connRegister:     make(chan connection.Connection),
	localHandlers:    make(map[string]localHandler),
}

func setupGamehub() {
	gamehub.localHandlers["clientInfo"] = gamehub.handleClientInfo
	hookupLobbyHandlers()

	go gamehub.processNewGameRequests()
}

func hookupLobbyHandlers() {
	gamehub.localHandlers["newGame"] = gamehub.handleNewGame
	gamehub.localHandlers["killClient"] = gamehub.handleDisconnection
}
