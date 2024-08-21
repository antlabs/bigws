# greatws
A websocket library that allegedly can support massive connections, async in nature, supporting callbacks  
A fork from https://github.com/antlabs/greatws  
// 支持海量连接的websocket库，callback写法

# Main Features
//# 特性  
* based on epoll/kqueue  
//* 支持 epoll/kqueue  
* low memory usage  
//* 低内存占用  
* high transaction (tps transactions per seconds) volume  
//* 高tps  

# Still todo    
//# 暂不支持  
* ssl
* windows
* io-uring

# Warning  
//# 警告⚠️  
Early stages, production use is not recommended!  
// 早期阶段，暂时不建议生产使用  

# Example Server
//# 例子-服务端  
```go

type echoHandler struct{}

func (e *echoHandler) OnOpen(c *greatws.Conn) {
	// fmt.Printf("OnOpen: %p\n", c)
}

func (e *echoHandler) OnMessage(c *greatws.Conn, op greatws.Opcode, msg []byte) {
	if err := c.WriteTimeout(op, msg, 3*time.Second); err != nil {
		fmt.Println("write fail:", err)
	}
	// if err := c.WriteMessage(op, msg); err != nil {
	// 	slog.Error("write fail:", err)
	// }
}

func (e *echoHandler) OnClose(c *greatws.Conn, err error) {
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	slog.Error("OnClose:", errMsg)
}

type handler struct {
	m *greatws.MultiEventLoop
}

func (h *handler) echo(w http.ResponseWriter, r *http.Request) {
	c, err := greatws.Upgrade(w, r,
		greatws.WithServerReplyPing(),
		// greatws.WithServerDecompression(),
		greatws.WithServerIgnorePong(),
		greatws.WithServerCallback(&echoHandler{}),
		// greatws.WithServerEnableUTF8Check(),
		greatws.WithServerReadTimeout(5*time.Second),
		greatws.WithServerMultiEventLoop(h.m),
	)
	if err != nil {
		slog.Error("Upgrade fail:", "err", err.Error())
	}
	_ = c
}

func main() {

	var h handler

	h.m = greatws.NewMultiEventLoopMust(greatws.WithEventLoops(0), greatws.WithMaxEventNum(1000), greatws.WithLogLevel(slog.LevelError)) // epoll, kqueue
	h.m.Start()
	fmt.Printf("apiname:%s\n", h.m.GetApiName())

	mux := &http.ServeMux{}
	mux.HandleFunc("/autobahn", h.echo)

	rawTCP, err := net.Listen("tcp", ":9001")
	if err != nil {
		fmt.Println("Listen fail:", err)
		return
	}
}
```
