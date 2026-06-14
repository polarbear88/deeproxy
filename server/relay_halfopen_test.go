package server

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// relay_halfopen_test.go 覆盖 T2.2 (DEC-D1) relay 半开语义回归：
//   ① 上传方向先 EOF，下载方向应继续不被中断（保活「请求小、响应大」）；
//   ② 任一方向出错，两端 conn 应被立即关闭、两个方向 goroutine 都回收（无泄漏/无挂起）。
//
// 用真实 TCP 连接（net.Pipe 不支持 CloseWrite 的半关语义），贴近生产行为。

// tcpPair 建立一对已连接的 *net.TCPConn（a<->b）。
func tcpPair(t *testing.T) (a, b net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()

	type res struct {
		c   net.Conn
		err error
	}
	accepted := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		accepted <- res{c, err}
	}()

	dialed, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	ar := <-accepted
	if ar.err != nil {
		t.Fatalf("Accept 失败: %v", ar.err)
	}
	return dialed, ar.c
}

// TestRelayCounted_UploadEOFFirst_DownloadContinues 验证：
// 上行方向客户端先发完并半关（EOF），下行方向应继续传输，不被中断（持续 ~1s 后才结束）。
func TestRelayCounted_UploadEOFFirst_DownloadContinues(t *testing.T) {
	// 拓扑：clientConn <-> (relay) <-> targetConn
	//   relayCounted(clientW=clientConn, clientR=clientConn, target=targetConn)
	clientConn, clientPeer := tcpPair(t) // clientPeer 模拟真实客户端
	targetConn, targetPeer := tcpPair(t) // targetPeer 模拟上游/目标服务器

	relErrCh := make(chan error, 1)
	var up, down int64
	go func() {
		var err error
		up, down, err = relayCounted(clientConn, clientConn, targetConn)
		relErrCh <- err
	}()

	// 客户端：发一小段上行数据后立即半关写方向（模拟「请求已发完」）。
	const upPayload = "GET /big HTTP/1.1\r\n\r\n"
	if _, err := clientPeer.Write([]byte(upPayload)); err != nil {
		t.Fatalf("客户端写上行失败: %v", err)
	}
	if cw, ok := clientPeer.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite() // 上行 EOF：客户端不再发送
	}

	// 目标：在上行 EOF 之后，分多次、跨越 ~1s 持续下发数据，验证下载不被上行 EOF 切断。
	const chunks = 5
	const chunkSize = 4096
	go func() {
		for i := 0; i < chunks; i++ {
			time.Sleep(200 * time.Millisecond)
			buf := make([]byte, chunkSize)
			if _, err := targetPeer.Write(buf); err != nil {
				return
			}
		}
		// 下发完毕，半关下行写方向（EOF），让中继正常收尾；用 CloseWrite 而非 Close
		// 以发送干净的 FIN（Close 在仍有半关状态时可能触发 RST，污染本用例）。
		if cw, ok := targetPeer.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = targetPeer.Close()
		}
	}()

	// 客户端侧读取全部下行数据并计数；若中继被错误切断，会提前 EOF、字节数不足。
	readDone := make(chan int64, 1)
	go func() {
		n, _ := io.Copy(io.Discard, clientPeer)
		readDone <- n
	}()

	select {
	case err := <-relErrCh:
		if err != nil {
			t.Fatalf("relayCounted 不应返回错误（正常半开收尾），却得到: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relayCounted 超时未返回（下行可能被错误切断或挂起）")
	}

	gotDown := <-readDone
	wantDown := int64(chunks * chunkSize)
	if gotDown != wantDown {
		t.Fatalf("下行字节被截断：客户端收到 %d，期望 %d（上行 EOF 误切了下行）", gotDown, wantDown)
	}
	if down != wantDown {
		t.Fatalf("relayCounted 统计下行 %d，期望 %d", down, wantDown)
	}
	if up != int64(len(upPayload)) {
		t.Fatalf("relayCounted 统计上行 %d，期望 %d", up, len(upPayload))
	}

	_ = clientConn.Close()
	_ = targetConn.Close()
}

// errConn 包一个 net.Conn，使其 Read 立即返回指定错误（模拟某一方向出错）。
// 其余方法透传底层 conn，以便 Close 能真实关闭、解除另一方向阻塞。
type errConn struct {
	net.Conn
	readErr error
	once    sync.Once
	fired   chan struct{}
}

func newErrConn(c net.Conn, readErr error) *errConn {
	return &errConn{Conn: c, readErr: readErr, fired: make(chan struct{})}
}

func (e *errConn) Read(p []byte) (int, error) {
	e.once.Do(func() { close(e.fired) })
	return 0, e.readErr
}

// TestRelayCounted_OneSideError_BothReclaimed 验证：
// 下行方向（target.Read）立即出错时，relayCounted 应关闭两端，
// 解除上行方向阻塞的 Read（客户端不发数据、不 EOF），使函数在有限时间内返回错误。
func TestRelayCounted_OneSideError_BothReclaimed(t *testing.T) {
	clientConn, clientPeer := tcpPair(t)
	targetConn, _ := tcpPair(t)

	// 让 target 方向（下行 io.Copy(clientW, target)）的 Read 立即出错。
	wantErr := errors.New("模拟下行读错误")
	badTarget := newErrConn(targetConn, wantErr)

	relErrCh := make(chan error, 1)
	go func() {
		_, _, err := relayCounted(clientConn, clientConn, badTarget)
		relErrCh <- err
	}()

	// 客户端【既不发送也不 EOF】：上行方向 io.Copy(target, clientR) 会阻塞在 clientConn.Read。
	// 若 relayCounted 未在出错时关两端，上行将永久阻塞、函数永不返回 → 超时失败（验证泄漏防护）。
	defer clientPeer.Close()

	select {
	case err := <-relErrCh:
		if err == nil {
			t.Fatal("出错方向应使 relayCounted 返回非 nil error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relayCounted 出错后未关两端：上行阻塞 Read 未被解除（fd 泄漏风险）")
	}

	// 关两端后，客户端连接应已被关闭（验证「关两端」确实触达客户端 conn）。
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := clientConn.Read(buf); err == nil {
		t.Fatal("客户端 conn 应已被 relayCounted 关闭")
	}
}
