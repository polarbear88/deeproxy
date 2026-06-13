package dialer

import (
	"net"
	"testing"
	"time"
)

// TestWrapIdleTimeout 覆盖 AC10b：空闲超过阈值后 Read 应因超时返回错误。
func TestWrapIdleTimeout(t *testing.T) {
	// 用一对内存管道连接模拟双向连接：server 端不发数据，制造“空闲”。
	client, server := net.Pipe()
	defer server.Close()

	wrapped := WrapIdle(client, 50*time.Millisecond)
	defer wrapped.Close()

	// 不向 wrapped 写入任何数据；等待超过 idle 阈值后读应超时。
	buf := make([]byte, 16)
	_, err := wrapped.Read(buf)
	if err == nil {
		t.Fatal("空闲超时后 Read 期望返回错误，但成功了")
	}
	netErr, ok := err.(net.Error)
	if !ok || !netErr.Timeout() {
		t.Fatalf("期望超时错误，实际: %v", err)
	}
}

// TestWrapIdleZero 覆盖：idle<=0 时不包装，原样返回。
func TestWrapIdleZero(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	if got := WrapIdle(client, 0); got != client {
		t.Fatal("idle<=0 时应原样返回原连接")
	}
}

// TestWrapIdleResetsOnRead 覆盖：有数据往来时空闲计时被刷新，不应误超时。
func TestWrapIdleResetsOnRead(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	wrapped := WrapIdle(client, 100*time.Millisecond)

	// server 端周期性发送数据，client 端持续能读到，不应超时。
	go func() {
		for range 3 {
			time.Sleep(40 * time.Millisecond)
			_, _ = server.Write([]byte("x"))
		}
	}()

	buf := make([]byte, 1)
	for i := range 3 {
		if _, err := wrapped.Read(buf); err != nil {
			t.Fatalf("第 %d 次读在持续有数据时不应出错: %v", i+1, err)
		}
	}
}
