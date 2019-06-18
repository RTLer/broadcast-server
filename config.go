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