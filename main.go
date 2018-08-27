package main

import (
	"sync"
	"github.com/gorilla/websocket"
	"github.com/garyburd/redigo/redis"
	"net/http"
	"log"
	"github.com/satori/go.uuid"
	"time"
	"errors"
	"net"
	"io/ioutil"
	"encoding/json"
)

var (
	gStore      *Store
	gPubSubConn *redis.PubSubConn
	gRedisConn  = func() (redis.Conn, error) {
		return redis.Dial("tcp", ":6378")
	}
	serverAddress = ":8081"
	subs          = subscribscription{
		channels: []string{},
	}
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
)

type User struct {
	ID       string
	channels []string
	conn     *websocket.Conn
}

type Store struct {
	Users []*User
	sync.Mutex
}

type Message struct {
	DeliveryID string `json:"id"`
	Content    string `json:"content"`
}

type AuthChannels struct {
	Channels []string `json:"data"`
}

type subscribscription struct {
	channels []string
}

func init() {
	gStore = &Store{
		Users: make([]*User, 0, 1),
	}
}

func (s *Store) newUser(conn *websocket.Conn, trackId string) *User {
	userUuid, _ := uuid.NewV4()
	var channels []string
	if trackId != "" {
		channels = []string{"tracker:" + trackId}
	}

	u := &User{
		ID:       userUuid.String(),
		channels: channels,
		conn:     conn,
	}

	s.Lock()
	defer s.Unlock()

	s.Users = append(s.Users, u)
	return u
}

func main() {
	gRedisConn, err := gRedisConn()
	if err != nil {
		panic(err)
	}
	defer gRedisConn.Close()

	gPubSubConn = &redis.PubSubConn{Conn: gRedisConn}
	defer gPubSubConn.Close()
	if err := gPubSubConn.Subscribe("all"); err != nil {
		panic(err)
	}

	go deliverMessages()

	http.HandleFunc("/api/ws/", wsHandler)

	log.Printf("server started at %s\n", serverAddress)
	log.Fatal(http.ListenAndServe(serverAddress, nil))
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrader error %s\n", err.Error())
		return
	}

	trackId := r.URL.Path[len("/api/ws/"):]
	u := gStore.newUser(conn, trackId)
	go func(u *User) {
		err := u.subscribeUser(r)
		if err != nil {
			log.Printf("%s\n" + err.Error())
		}
	}(u)

	log.Printf("user %s joined\n", u.ID)
	i := 0
	for {
		var m Message
		if err := u.conn.ReadJSON(&m); err != nil {
			i++
			if i >= 10 {
				log.Printf("error on ws. message %s\n", err.Error())
				subs.unsub(u)
				u.conn.Close()
				break
			}
		} else {
			log.Printf("message %s\n", string(m.Content))
		}

		if c, err := gRedisConn(); err != nil {
			log.Printf("error on redis conn. %s\n", err)
		} else {
			c.Do("PUBLISH", m.DeliveryID, string(m.Content))
		}
	}
}

func (sch *subscribscription) sub(u *User) error {
	for _, ch := range u.channels {
		if err := gPubSubConn.Subscribe(ch); err != nil {
			return errors.New("redis subscribe error")
		}
		sch.channels = append(sch.channels, ch)
	}
	return nil
}

func (sch *subscribscription) unsub(u *User) error {
	for _, ch := range u.channels {
		if err := gPubSubConn.Unsubscribe(ch); err != nil {
			return errors.New("redis unsubscribe error")
		}
		for s, cha := range sch.channels {
			if cha == ch {
				sch.channels = append(sch.channels[:s], sch.channels[s+1:]...)
			}
		}
	}
	return nil
}

func (u *User) subscribeUser(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		cookiesAuth, err := r.Cookie("access_token")
		if err != nil {
			log.Printf("unauth request %s\n" + err.Error())
			return errors.New("auth failed")
		}
		auth = "Bearer " + cookiesAuth.Value
	}

	var netTransport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	netClient := &http.Client{
		Timeout:   time.Second * 10,
		Transport: netTransport,
	}

	req, _ := http.NewRequest("GET", "http://localhost:8003/api/broadcast/channels", nil)
	req.Header.Set("Authorization", auth)
	response, err := netClient.Do(req)

	if err != nil {
		return errors.New("auth request failed")
	}

	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("auth request failed")
	}
	bodyBytes, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return errors.New("auth request parse failed")
	}
	res := AuthChannels{}
	json.Unmarshal(bodyBytes, &res)
	for _, channel := range res.Channels {
		u.channels = append(u.channels, channel)
	}

	subs.sub(u)

	return nil
}

func deliverMessages() {
	for {
		switch v := gPubSubConn.Receive().(type) {
		case redis.Message:
			log.Printf("subscription message: %s: %s\n", v.Channel, v.Data)
			gStore.findAndDeliver(v.Channel, string(v.Data))
		case redis.Subscription:
			log.Printf("subscription message: %s: %s %d\n", v.Channel, v.Kind, v.Count)
		case error:
			log.Println("error pub/sub on connection, delivery has stopped")
			return
		}
	}
}

func (s *Store) findAndDeliver(redisChannel string, content string) {
	deliveryUuid, _ := uuid.NewV4()
	m := Message{
		DeliveryID: deliveryUuid.String(),
		Content:    content,
	}
	for _, u := range s.Users {
		if "all" == redisChannel {
			if err := u.conn.WriteJSON(m); err != nil {
				log.Printf("error on message delivery through ws. e: %s\n", err)
			} else {
				log.Printf("user %s found at our store, message sent\n", redisChannel)
			}
			continue
		}
		for _, channel := range u.channels {
			if channel == redisChannel {
				if err := u.conn.WriteJSON(m); err != nil {
					log.Printf("error on message delivery through ws. e: %s\n", err)
				} else {
					log.Printf("user %s found at our store, message sent\n", redisChannel)
				}
			}
		}
	}
}
