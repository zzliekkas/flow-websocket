# flow-websocket
 
 WebSocket client/server utilities extracted from `github.com/zzliekkas/flow/v2/websocket`.
 
 ## Install
 
 ```bash
 go get github.com/zzliekkas/flow-websocket@v0.1.0
 ```
 
 ## Usage
 
 ```go
 package main
 
 import websocket "github.com/zzliekkas/flow-websocket"
 
 func main() {
 	_ = websocket.NewManager()
 }
 ```
