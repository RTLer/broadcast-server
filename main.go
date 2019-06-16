package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/garyburd/redigo/redis"
	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

func main() {
	flags()

	//go func() {
	//	mux := http.NewServeMux()
	//	mux.HandleFunc("/custom_debug_path/profile", pprof.Profile)
	//	log.Fatal(http.ListenAndServe(":7777", mux))
	//}()

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

	if *BroadcastStats {
		serverStatusTicker()
	}

	go deliverMessages()

	http.HandleFunc("/api/ws/", wsHandler)
	http.HandleFunc("/api/ws/auth", authHandler)
	http.HandleFunc("/api/ws/internal/stats", statsHandler)

	logrus.Infof("Server started at %s\n", *serverAddress)
	logrus.Fatal(http.ListenAndServe(*serverAddress, nil))
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	logrus.Info("Users Count:", len(gStore.Users))

	statsMessage := getServerStats()
	res, jErr := json.Marshal(statsMessage)
	if jErr != nil {
		logrus.Error(jErr)
	}

	if _, err := w.Write(res); err != nil {
		logrus.Error(err)
	}
}

func requestHandler(postData []byte, reqHandleTryCounter int) {
	req, _ := http.NewRequest("POST", *webhookUrl, bytes.NewBuffer(postData))
	req.Header.Set("Content-Type", "application/json")

	var netTransport = &http.Transport {
		DialContext: (&net.Dialer {
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
		reqHandleTryCounter++
		time.Sleep(5 * time.Second)
		if reqHandleTryCounter <= 10 {
			logrus.Infof("Remaining : %d times. Retry http request: %s\n", 10 - reqHandleTryCounter, err.Error())
			requestHandler(postData, reqHandleTryCounter)
		}

		return
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		reqHandleTryCounter++
		logrus.Printf("got wrong http code(retry): %v\n", response.StatusCode)
		time.Sleep(5 * time.Second)
		if reqHandleTryCounter <= 10 {
			requestHandler(postData, reqHandleTryCounter)
		}

		return
	}

}

func authHandler(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)

	var data authInfo
	err := decoder.Decode(&data)
	if err != nil {
		logrus.Error(err.Error())
	}

	if data.ClientId != "" {
		for _, u := range gStore.Users {
			if u.ID == data.ClientId {
				u.userId = data.ClientId
				if data.Otp != "" {
					clientIdM := Message{
						Command: "Otp",
						Content: data.Otp,
					}
					clientIdM.signMessage()
					if err := u.conn.WriteJSON(clientIdM); err != nil {
						logrus.Printf("error on message delivery through ws. e: %s\n", err)
					}
				}
			}
		}
	}
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.Printf("Upgrader error %s\n", err.Error())

		return
	}

	trackId := r.URL.Path[len("/api/ws/"):]
	u := gStore.NewUser(conn, trackId)
	err = subs.sub(u)
	if err != nil {
		logrus.Printf("%s\n" + err.Error())
	}

	clientIdM := Message{
		Content: u.ID,
		Command: "ClientID",
	}

	clientIdM.signMessage()
	if err := u.conn.WriteJSON(clientIdM); err != nil {
		logrus.Printf("error on message delivery through ws. e: %s\n", err)
	}

	logrus.Printf("user %s joined\n", u.ID)
	i := 0

	wg := sync.WaitGroup{}
	wg.Add(4)
	//for {
	go func() {
		var m Message
		if err := u.conn.ReadJSON(&m); err != nil {
			i++
			if i >= 10 {
				log.Printf("error on ws. message: %s\n", err.Error())
				if err := subs.unsub(u); err != nil {
					logrus.Errorf("Error on unsubscribe: %v", err)
				}

				gStore.RemoveUser(u)
				if err := u.conn.Close(); err != nil {
					logrus.Errorf("Error on close connection %v", err)
				}
				//break
			}

			logrus.Errorf("Removed User on connection problem %s\n", string(err.Error()))
		} else {
			switch m.Command {
			case "auth":
				authM := Message{
					Content: "auth successful",
					Command: "AuthSuccess",
				}
				authM.signMessage()

				if err := u.authUser(r, m); err != nil {
					logrus.Error(err.Error())
					//logrus.Errorf("error auth command: %v\n", err.Error())
					authM.Content = "auth failed"
					authM.Command = "AuthFailed"
				}

				if err := u.conn.WriteJSON(authM); err != nil {
					logrus.Errorf("error on message delivery through ws. e: %s\n", err)
				}

				break
			default:
				go callWebhook(u, m)
			}
		}

		if c, err := gRedisConn(); err != nil {
			logrus.Errorf("error on redis conn. %s\n", err)
		} else {
			if _, err := c.Do("PUBLISH", m.DeliveryID, string(m.Content)); err != nil {
				logrus.Errorf("Error redis publish. %s\n", err)
			}
		}
	}()

	//}

	wg.Wait()
}

func (sch *subscription) sub(u *User) error {
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
		logrus.Printf("subscribed to %s\n", userChannel)
	}

	return nil
}

func (sch *subscription) unsub(u *User) error {
UnSubscriptionLoop:
	for _, userChannel := range u.channels {
		for _, user := range gStore.Users {
			if user.ID != u.ID {
				for _, otherUsersChannel := range user.channels {
					if userChannel == otherUsersChannel {
						logrus.Printf("another user subscribed to \"%s\" channel\n", userChannel)
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

func callWebhook(u *User, m Message) {
	postData, _ := json.Marshal(
		WebhookMessage{
			UserId:  u.userId,
			Message: m,
		})

	requestHandler(postData, 0)
}

func deliverMessages() {
	for {
		switch v := gPubSubConn.Receive().(type) {
		case redis.Message:
			logrus.Infof("Subscription message: %s: %s\n", v.Channel, v.Data)
			go gStore.findAndDeliver(v.Channel, v.Data)
		case redis.Subscription:
			logrus.Infof("Subscription message: %s: %s %d\n", v.Channel, v.Kind, v.Count)
		case error:
			logrus.Error("Error pub/sub on connection, delivery has stopped")
			return
		}
	}
}

func (s *Store) findAndDeliver(redisChannel string, content []byte) {
	m := Message{}
	if err := json.Unmarshal(content, &m); err != nil {
		logrus.Error("Message format is not valid: %s\n", string(content))
		return
	}

	for _, u := range s.Users {
		if redisChannel == "public.all" {
			if err := u.conn.WriteJSON(m); err != nil {
				logrus.Error("Error on message delivery through ws. e: %s\n", err)
			}
			continue
		} else {
			for _, channel := range u.channels {
				if channel == redisChannel {
					if err := u.conn.WriteJSON(m); err != nil {
						logrus.Error("Error on message delivery through ws. e: %s\n", err)
					}
				}
			}
		}
	}
}

func (m *Message) signMessage() {
	deliveryUuid, _ := uuid.NewV4()
	m.DeliveryID = deliveryUuid.String()
}

func serverStatusTicker() {
	ticker := time.NewTicker(10 * time.Second)

	go func() {
		for range ticker.C {
			statsJson, _ := json.Marshal(getServerStats())
			go gStore.findAndDeliver("public.all", statsJson)
		}
	}()
}

func getServerStats() Message {
	statsMessage := Message {
		Command: "ServerStats",
		Data: StatsData {
			UserCount: len(gStore.Users),
		},
	}

	statsMessage.signMessage()

	return statsMessage
}
