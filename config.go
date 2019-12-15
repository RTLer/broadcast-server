package main

import "flag"

func flags() {
	BroadcastStats = flag.Bool(
		"BroadcastStats",
		false,
		"Broadcast stats",
	)

	serverAddress = flag.String(
		"serverAddress",
		":8081",
		"ws address",
	)

	authUrl = flag.String(
		"authUrl",
		"http://localhost:8080/api/broadcast/auth",
		"auth url",
	)

	sentryDSN = flag.String(
		"sentryDSN",
		"https://user:pass@sentry.example.com/20",
		"auth url",
	)

	webhookUrl = flag.String(
		"webhookUrl",
		"http://localhost:8080/api/broadcast/webhook",
		"webhook url",
	)

	queuesFlag = flag.String(
		"queues",
		"default,",
		"RabbitMQ queues. like: transactions,tickets. split with comma",
	)

	rabbitEndPoint = flag.String(
		"rabbit",
		"amqp://broadcast-user:broadcast-user@localhost:5672/",
		"RabbitMQ connection url",
	)

	debug = flag.Bool(
		"debug",
		false,
		"open debug mode",
	)

	//publicChannelsUrl = *flag.String(
	//	"publicChannelsUrl",
	//	"http://localhost:8003/api/broadcast/publicChannels",
	//	"public channels url",
	//)

	flag.Parse()
}