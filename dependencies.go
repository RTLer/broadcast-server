package main

import (
	"github.com/gorilla/websocket"
	"github.com/streadway/amqp"
	"net/http"
)

var (
	gStore     *Store
	queuesFlag *string //like: success,failed, ... | split with comma
	queues     []string

	BroadcastStats *bool
	serverAddress  *string
	authUrl        *string
	webhookUrl     *string
	sentryDSN      *string
	debug          *bool
	rabbitEndPoint *string

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	rabbitMQ *amqp.Connection
)

type Message struct {
	Channel    string      `json:"channel"`
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

type UserPublic struct {
	ID       string `json:"id"`
	UserId   string `json:"user_id"`
	Channels []string `json:"channels"`
}

type AuthChannels struct {
	UserId   string   `json:"user_id"`
	Channels []string `json:"channels"`
}
