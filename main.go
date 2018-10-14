package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"github.com/garyburd/redigo/redis"
	"github.com/gorilla/websocket"
	"github.com/satori/go.uuid"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	gStore       *Store
	gPubSubConn  *redis.PubSubConn
	redisAddress *string
	gRedisConn   = func() (redis.Conn, error) {
		return redis.Dial("tcp", *redisAddress)
	}
	serverAddress *string
	authUrl       *string
	webhookUrl    *string
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
	userId   string
	channels []string
	conn     *websocket.Conn
}

type Store struct {
	Users []*User
	sync.Mutex
}

type Message struct {
	DeliveryID string      `json:"id"`
	Content    string      `json:"content"`
	Command    string      `json:"command"`
	Data       interface{} `json:"data"`
}

func (m *Message) sign() {
	deliveryUuid, _ := uuid.NewV4()
	m.DeliveryID = deliveryUuid.String()

}

type WebhookMessage struct {
	UserId  string  `json:"user_id"`
	Message Message `json:"message"`
}

type authInfo struct {
	UserId   string `json:"user_id"`
	ClientId string `json:"client_id"`
	Otp      string `json:"otp"`
}

type AuthChannels struct {
	UserId   string   `json:"user_id"`
	Channels []string `json:"channels"`
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
		channels = []string{"direct." + userUuid.String(), "direct." + trackId}
	} else {
		channels = []string{"direct." + userUuid.String()}
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

	serverAddress = flag.String(
		"serverAddress",
		":8081",
		"ws address",
	)
	redisAddress = flag.String(
		"redisAddress",
		":6379",
		"redis connection",
	)
	authUrl = flag.String(
		"authUrl",
		"http://localhost:8080/api/broadcast/auth",
		"auth url",
	)

	webhookUrl = flag.String(
		"webhookUrl",
		"http://localhost:8080/api/broadcast/webhook",
		"webhook url",
	)

	flag.Parse()

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
	http.HandleFunc("/api/ws/auth", authHandler)

	log.Printf("server started at %s\n", *serverAddress)
	log.Fatal(http.ListenAndServe(*serverAddress, nil))
}

func authHandler(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)

	var data authInfo
	err := decoder.Decode(&data)
	if err != nil {
		log.Printf(err.Error())
	}

	if data.ClientId != "" {
		for _, u := range gStore.Users {
			if u.ID == data.ClientId {
				u.userId = data.ClientId
				if data.Otp != "" {
					clientIdM := Message{
						Content: data.Otp,
						Command: "Otp",
					}
					clientIdM.sign()
					if err := u.conn.WriteJSON(clientIdM); err != nil {
						log.Printf("error on message delivery through ws. e: %s\n", err)
					}
				}
			}
		}
	}
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrader error %s\n", err.Error())
		return
	}

	trackId := r.URL.Path[len("/api/ws/"):]
	u := gStore.newUser(conn, trackId)
	err = subs.sub(u)
	if err != nil {
		log.Printf("%s\n" + err.Error())
	}

	clientIdM := Message{
		Content: u.ID,
		Command: "ClientID",
	}
	clientIdM.sign()
	if err := u.conn.WriteJSON(clientIdM); err != nil {
		log.Printf("error on message delivery through ws. e: %s\n", err)
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
			log.Printf("error %s\n", string(err.Error()))

		} else {
			content, _ := json.Marshal(m)
			switch m.Command {
			case "auth":
				authM := Message{
					Content: "auth successful",
					Command: "AuthSuccess",
				}
				authM.sign()

				if err := u.authUser(r, m); err != nil {
					log.Printf("error auth command: %s\n", string(err.Error()))
					authM.Content = "auth failed"
					authM.Command = "AuthFailed"
				}

				if err := u.conn.WriteJSON(authM); err != nil {
					log.Printf("error on message delivery through ws. e: %s\n", err)
				}
				break
			default:
				go callWebhook(u, m)
			}
			log.Printf("message %s\n", content)
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

func (u *User) authUser(r *http.Request, m Message) error {
	log.Printf("auth user")
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

	postData, _ := json.Marshal(m)
	req, _ := http.NewRequest("POST", *authUrl, bytes.NewBuffer(postData))
	req.Header.Set("Content-Type", "application/json")
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

	authRes := AuthChannels{}
	json.Unmarshal(bodyBytes, &authRes)

	if authRes.UserId != "" {
		u.userId = authRes.UserId
	}

	for _, channel := range authRes.Channels {
		u.channels = append(u.channels, "private."+string(channel))
	}

	subs.sub(u)
	return nil
}
func callWebhook(u *User, m Message) error {

	webhookMessage := WebhookMessage{
		UserId:  u.userId,
		Message: m,
	}
	postData, _ := json.Marshal(webhookMessage)
	req, _ := http.NewRequest("POST", *webhookUrl, bytes.NewBuffer(postData))
	req.Header.Set("Content-Type", "application/json")
	requestHandler(req)
	return nil
}

func requestHandler(req *http.Request) {
	var netTransport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 60 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 60 * time.Second,
	}

	netClient := &http.Client{
		Timeout:   time.Second * 60,
		Transport: netTransport,
	}
	response, err := netClient.Do(req)
	if err != nil {
		log.Printf("retry http request: %s\n", err.Error())
		time.Sleep(5 * time.Second)
		requestHandler(req)
		return
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		log.Printf("got wrong http code(retry): %s\n" ,string(http.StatusOK))
		time.Sleep(5 * time.Second)
		requestHandler(req)
		return
	}
}

func deliverMessages() {
	for {
		switch v := gPubSubConn.Receive().(type) {
		case redis.Message:
			log.Printf("subscription message: %s: %s\n", v.Channel, v.Data)
			go gStore.findAndDeliver(v.Channel, v.Data)
		case redis.Subscription:
			log.Printf("subscription message: %s: %s %d\n", v.Channel, v.Kind, v.Count)
		case error:
			log.Println("error pub/sub on connection, delivery has stopped")
			return
		}
	}
}

func (s *Store) findAndDeliver(redisChannel string, content []byte) {
	m := Message{}
	if err := json.Unmarshal(content, &m);err != nil{
		log.Printf("message format is not valid: %s\n",string(content))
		return
	}
	m.sign()
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
