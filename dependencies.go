package main

import (
	"github.com/garyburd/redigo/redis"
	"github.com/gorilla/websocket"
	"net/http"
	"sync"
)

var (
	gStore       *Store
	gPubSubConn  *redis.PubSubConn
	redisAddress *string
	gRedisConn   = func() (redis.Conn, error) {
		return redis.Dial("tcp", *redisAddress)
	}
	BroadcastStats *bool
	serverAddress  *string
	authUrl        *string
	webhookUrl     *string
	debug          *bool
	subs = subscription{
		Channels: []string{},
	}
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
)

type Message struct {
	DeliveryID string      `json:"id"`
	Content    string      `json:"content"`
	Command    string      `json:"command"`
	Data       interface{} `json:"data"`
}

type WebhookMessage struct {
	UserId  string  `json:"user_id"`
	Message Message `json:"message"`
}

type StatsData struct {
	UserCount int `json:"user_count"`
}

type AuthChannels struct {
	UserId   string   `json:"user_id"`
	Channels []string `json:"channels"`
}

type subscription struct {
	Channels []string `json:"Channels"`
	sync.Mutex
}