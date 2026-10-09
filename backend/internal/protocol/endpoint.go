package protocol

// Endpoint 指定监听地址及对应适配器，由 gateway 运行时启动监听。
type Endpoint struct {
	Address string
	Adapter Adapter
}
