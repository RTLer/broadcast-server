package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"
	"io/ioutil"
	"net"
	"net/http"
	"time"
)

type User struct {
	ID       string
	userId   string
	channels []string
	conn     *websocket.Conn
}

func (s *Store) NewUser(conn *websocket.Conn, trackId string) *User {
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


func (s *Store) RemoveUser(u *User) {
	for index, user := range s.Users {
		if user.ID == u.ID {
			s.Lock()
			s.Users = append(s.Users[:index], s.Users[index+1:]...)
			s.Unlock()
		}
	}
}

func (u *User) authUser(r *http.Request, m Message) error {
	logrus.Info("Auth User")
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
	if err := json.Unmarshal(bodyBytes, &authRes); err != nil {
		logrus.Errorf("Error on decode auth user: %v", err)
	}

	if authRes.UserId != "" {
		u.userId = authRes.UserId
	}

	for _, channel := range authRes.Channels {
		u.channels = append(u.channels, "private."+string(channel))
	}

	if err := subs.sub(u); err != nil {
		logrus.Errorf("Error on subscribe: %v", err)
	}

	return nil
}