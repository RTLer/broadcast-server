# Broadcast Server


run with docker


this service run on redis for publish messages 


# How it`s run?
```
go run *.go -BroadcastStats=true -serverAddress=8080 -redisAddress=6370 -authUrl=127.0.0.1/api/auth -webhookUrl=127.0.0.1/api/webhook
```

# flags describe:


* **BroadcastStats**: this flag as default is false, if you set this on true tick online users every 10 seconds.
* **serverAddress**: server port. default is on :8081
* **redisAddress**: set redist connection port, default is on :6379
* **authUrl**: url of your client server for auth users
* **webhookUrl**: webhook url that broadcast server call that on any action 


# data structure for input/output

```json
{
  "id": "51fc8126-221e-43fc-abee-91a66fce4fa9",//uuid string
  "command": "auth",
  "content": "a message title",//string
  "data": {}//object
}

```
