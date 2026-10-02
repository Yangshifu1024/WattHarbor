package protocol

// Endpoint 描述一个协议监听端口及其协议适配器。
// 它是跨进程契约的一部分：适配器由各协议子目录提供，监听由 gateway 运行时
// 建立，两边通过这个结构对接。
type Endpoint struct {
	Address string
	Adapter Adapter
}
