package main

import (
	"bytes"
	"encoding/json"
	"github.com/getsentry/raven-go"
	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"net"
	"net/http"
	"strings"
	"time"
)

func init() {
	flags()

	gStore = &Store{
		Users: make([]*User, 0, 1),
	}
}

func main() {
	var err error
	rabbitMQ, err = amqp.Dial(*rabbitEndPoint)
	if err != nil {
		logrus.Fatalf("Failed to amqp.Dial to RabbitMQ: %s", err)
	}

	queues = strings.Split(*queuesFlag, ",")

	logrus.SetLevel(logrus.FatalLevel)
	if *debug {
		logrus.SetLevel(logrus.TraceLevel)
	} else {
		if err := raven.SetDSN("https://f4eebbf8fe9d4179a3884815c0055435:f6018bea50fe4c6a8d07a39aee13030c@sentry.zarinpal.com/20"); err != nil {
			logrus.Errorf("Sentry log error: %s", err)
		}
	}

	if *BroadcastStats {
		go serverStatusTicker()
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
		reqHandleTryCounter++
		time.Sleep(5 * time.Second)
		if reqHandleTryCounter <= 10 {
			raven.CaptureErrorAndWait(err, nil)
			//raven.CapturePanic(func() {
			//	// do all of the scary things here
			//}, nil)

			logrus.Infof("Remaining : %d times. Retry http request: %s\n", 10-reqHandleTryCounter, err.Error())
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

					writeJson(u, clientIdM)
				}
			}
		}
	}
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		raven.CaptureErrorAndWait(err, nil)
		logrus.Printf("Upgrader error %s\n", err.Error())

		return
	}

	trackId := r.URL.Path[len("/api/ws/"):]
	u := gStore.NewUser(conn, trackId)

	clientIdM := Message{
		Content: u.ID,
		Command: "ClientID",
	}

	clientIdM.signMessage()
	writeJson(u, clientIdM)

	logrus.Printf("user %s joined\n", u.ID)
	i := 1

	for {
		var m Message
		if err := u.conn.ReadJSON(&m); err != nil {
			i++
			if i > 10 {
				logrus.Errorf("error on ws. message: %s\n", err.Error())

				gStore.RemoveUser(u)
				logrus.Errorf("Removed User on connection problem %s\n", string(err.Error()))

				if err := u.conn.Close(); err != nil {
					logrus.Errorf("Error on close connection %v", err)
				}
				break
			}
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
					authM.Content = "auth failed"
					authM.Command = "AuthFailed"
				}

				if *debug {
					authM.Data = u.channels
				}

				writeJson(u, authM)

				break
			default:
				go callWebhook(u, m)
			}
		}
	}
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
	defer func() {
		if err := rabbitMQ.Close(); err != nil {
			logrus.Fatalf("Failed to rabbitMQ.Close: %s", err)
		}
	}()

	ch, chErr := rabbitMQ.Channel()
	if chErr != nil {
		logrus.Fatalf("Failed to open a channel: %s", chErr)
	}
	defer func() {
		if err := ch.Close(); err != nil {
			logrus.Fatalf("Failed to ch.Close: %s", err)
		}
	}()

	forever := make(chan bool)

	for _, qName := range queues {
		q, qErr := ch.QueueDeclare(
			qName, // name
			false, // durable
			false, // delete when unused
			false, // exclusive
			false, // no-wait
			nil,   // arguments
		)

		if qErr != nil {
			logrus.Fatalf("Failed to declare a queue: %s", qErr)
		}

		msgs, consumErr := ch.Consume(
			q.Name, // queue
			"",     // consumer
			true,   // auto-ack
			false,  // exclusive
			false,  // no-local
			false,  // no-wait
			nil,    // args
		)
		if consumErr != nil {
			logrus.Fatalf("Failed to register a consumer: %s", consumErr)
		}

		go func() {
			for d := range msgs {

				go gStore.findAndDeliver(d.Body)
			}
		}()
	}

	<-forever
}

func (s *Store) findAndDeliver(content []byte) {
	m := Message{}
	if err := json.Unmarshal(content, &m); err != nil {
		logrus.Error("Message format is not valid: %s\n", string(content))
		return
	}

	logrus.Info(m.Channel)
	usersLoop:
	for _, u := range s.Users {
		if m.Channel == "public.all" {
			writeJson(u, m)

			continue
		} else {
			for _, channel := range u.channels {
				if channel == m.Channel {
					writeJson(u, m)
					continue usersLoop
				}
			}
		}
	}
}

func writeJson(u *User, m Message) {
	u.connLock.Lock()
	defer u.connLock.Unlock()
	if err := u.conn.WriteJSON(m); err != nil {
		logrus.Errorf("Error on message delivery through ws. e: %v\n User: %s", err, u.ID)
		if err := u.conn.Close(); err != nil {
			raven.CaptureErrorAndWait(err, nil)
			logrus.Errorf("Error on close connection: %s", err)
		}

		gStore.RemoveUser(u)
	}
}

func (m *Message) signMessage() {
	deliveryUuid, _ := uuid.NewV4()
	m.DeliveryID = deliveryUuid.String()
}

func serverStatusTicker() {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		statsJson, _ := json.Marshal(getServerStats())
		go gStore.findAndDeliver(statsJson)
	}
}

func getServerStats() Message {
	statsMessage := Message{
		Channel: "public.all",
		Command: "ServerStats",
		Data: StatsData{
			UserCount: len(gStore.Users),
		},
	}

	statsMessage.signMessage()

	return statsMessage
}
