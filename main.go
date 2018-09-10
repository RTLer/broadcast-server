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
	"flag"
)

var (
	gStore      *Store
	gPubSubConn *redis.PubSubConn
	gRedisConn  = func() (redis.Conn, error) {
		return redis.Dial("tcp", ":6378")
	}
	authUrl string
	//publicChannelsUrl string
	subs = subscribscription{
		Channels: []string{},
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
	Channels []string `json:"Channels"`
}

type subscribscription struct {
	Channels []string `json:"Channels"`
	sync.Mutex
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
		channels = []string{"direct." + trackId}
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

func (s *Store) removeUser(u *User) {
	for index, user := range s.Users {
		if user.ID == u.ID {
			s.Lock()
			s.Users = append(s.Users[:index], s.Users[index+1:]...)
			s.Unlock()
		}
	}
}

func main() {

	redisServerAddress := *flag.String(
		"redisConnection",
		":8081",
		"redis connection",
	)
	authUrl = *flag.String(
		"authUrl",
		"http://localhost:8003/api/broadcast/myChannels",
		"auth url",
	)

	//publicChannelsUrl = *flag.String(
	//	"publicChannelsUrl",
	//	"http://localhost:8003/api/broadcast/publicChannels",
	//	"public channels url",
	//)

	gRedisConn, err := gRedisConn()
	if err != nil {
		panic(err)
	}
	defer gRedisConn.Close()

	gPubSubConn = &redis.PubSubConn{Conn: gRedisConn}
	defer gPubSubConn.Close()
	if err := gPubSubConn.Subscribe("public.all"); err != nil {
		panic(err)
	}

	go deliverMessages()

	http.HandleFunc("/api/ws/", wsHandler)

	log.Printf("server started at %s\n", redisServerAddress)
	log.Fatal(http.ListenAndServe(redisServerAddress, nil))
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrader error %s\n", err.Error())
		return
	}

	trackId := r.URL.Path[len("/api/ws/"):]
	u := gStore.newUser(conn, trackId)
	err = u.subscribeUser(r)
	if err != nil {
		log.Printf("%s\n" + err.Error())
	}

	log.Printf("user %s joined\n", u.ID)
	i := 0
	for {
		var m Message
		if err := u.conn.ReadJSON(&m); err != nil {
			i++
			if i >= 10 {
				log.Printf("error on ws. message %s\n", err.Error())
				subs.unsub(u)
				gStore.removeUser(u)
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

SubscriptionLoop:
	for _, userChannel := range u.channels {
		for _, subscribedChannel := range sch.Channels {
			if userChannel == subscribedChannel {
				log.Printf("has subscribtion %s\n", userChannel)
				continue SubscriptionLoop
			}
		}
		if err := gPubSubConn.Subscribe(userChannel); err != nil {
			return errors.New("redis subscribe error" + err.Error())
		}

		sch.Lock()
		sch.Channels = append(sch.Channels, userChannel)
		sch.Unlock()
		log.Printf("subscribed to %s\n", userChannel)

	}
	return nil
}

func (sch *subscribscription) unsub(u *User) error {
UnSubscriptionLoop:
	for _, userChannel := range u.channels {
		for _, user := range gStore.Users {
			if user.ID != u.ID {
				for _, otherUsersChannel := range user.channels {
					if userChannel == otherUsersChannel {
						log.Printf("another user subscribed to \"%s\" channel\n", userChannel)
						continue UnSubscriptionLoop
					}
				}
			}
		}

		if err := gPubSubConn.Unsubscribe(userChannel); err != nil {
			return errors.New("redis unsubscribe error")
		}
		for s, cha := range sch.Channels {
			if cha == userChannel {
				sch.Lock()
				sch.Channels = append(sch.Channels[:s], sch.Channels[s+1:]...)
				sch.Unlock()
			}
		}
	}
	return nil
}

func (u *User) subscribeUser(r *http.Request) error {
	noAuth := r.URL.Query().Get("noauth")
	if noAuth == "" {
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

		req, _ := http.NewRequest("GET", authUrl, nil)
		req.Header.Set("Authorization", auth)
		response, err := netClient.Do(req)

		if err != nil {
			return errors.New("auth request failed: " + err.Error())
		}

		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return errors.New("auth request got wrong http code: " + string(http.StatusOK))
		}
		bodyBytes, err := ioutil.ReadAll(response.Body)
		if err != nil {
			return errors.New("auth request parse failed: " + err.Error())
		}
		res := AuthChannels{}
		json.Unmarshal(bodyBytes, &res)
		for _, channel := range res.Channels {
			u.channels = append(u.channels, string(channel))
		}
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
		if "public.all" == redisChannel {
			if err := u.conn.WriteJSON(m); err != nil {
				log.Printf("error on message delivery through ws. e: %s\n", err)
			} else {
				log.Printf("user %s found at our store, message sent\n", redisChannel)
			}
			continue
		} else {
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
}
