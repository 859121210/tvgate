package stream

import (
	"context"
	"errors"
	"fmt"
	"github.com/qist/tvgate/logger"
	"github.com/qist/tvgate/monitor"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------
// StreamHub 管理 UDP/组播流多客户端
// ---------------------------
type StreamHub struct {
	Mu        sync.Mutex
	Clients   map[chan []byte]struct{}
	AddCh     chan chan []byte
	RemoveCh  chan chan []byte
	UdpConn   *net.UDPConn
	Closed    chan struct{}
	BufPool   *sync.Pool
	LastFrame []byte // 最近一帧，供秒开和热切换
}

var (
	Hubs   = make(map[string]*StreamHub)
	HubsMu sync.Mutex
)

// ---------------------------
// 创建 StreamHub
// ---------------------------
func NewStreamHub(udpAddr string, ifaces []string) (*StreamHub, error) {
	addr, err := net.ResolveUDPAddr("udp", udpAddr)
	if err != nil {
		return nil, err
	}

	var conn *net.UDPConn
	if len(ifaces) == 0 {
		conn, err = net.ListenMulticastUDP("udp", nil, addr)
		if err != nil {
			conn, err = net.ListenUDP("udp", addr)
			if err != nil {
				return nil, err
			}
		}
		logger.LogPrintf("🟢 监听 %s (默认接口)", udpAddr)
	} else {
		var lastErr error
		for _, name := range ifaces {
			iface, ierr := net.InterfaceByName(name)
			if ierr != nil {
				lastErr = ierr
				logger.LogPrintf("⚠️ 网卡 %s 不存在或不可用: %v", name, ierr)
				continue
			}
			conn, err = net.ListenMulticastUDP("udp", iface, addr)
			if err == nil {
				logger.LogPrintf("🟢 监听 %s@%s 成功", udpAddr, name)
				break
			}
			lastErr = err
			logger.LogPrintf("⚠️ 监听 %s@%s 失败: %v", udpAddr, name, err)
		}
		if conn == nil {
			conn, err = net.ListenUDP("udp", addr)
			if err != nil {
				return nil, fmt.Errorf("所有网卡监听失败且 UDP 监听失败: %v (last=%v)", err, lastErr)
			}
			logger.LogPrintf("🟡 回退为普通 UDP 监听 %s", udpAddr)
		}
	}

	_ = conn.SetReadBuffer(4 * 1024 * 1024) // 放大缓冲

	hub := &StreamHub{
		Clients:  make(map[chan []byte]struct{}),
		AddCh:    make(chan chan []byte),
		RemoveCh: make(chan chan []byte),
		UdpConn:  conn,
		Closed:   make(chan struct{}),
		BufPool:  &sync.Pool{New: func() any { return make([]byte, 2048) }},
	}

	go hub.run()
	go hub.readLoop()

	logger.LogPrintf("UDP 监听地址：%s ifaces=%v", udpAddr, ifaces)
	return hub, nil
}

// ---------------------------
// Hub 主循环
// ---------------------------
func (h *StreamHub) run() {
	for {
		select {
		case ch := <-h.AddCh:
			h.Mu.Lock()
			h.Clients[ch] = struct{}{}
			// 秒开：发最近一帧
			if h.LastFrame != nil {
				select {
				case ch <- h.LastFrame:
				default:
				}
			}
			h.Mu.Unlock()
			logger.LogPrintf("➕ 客户端加入，当前=%d", len(h.Clients))

		case ch := <-h.RemoveCh:
			h.Mu.Lock()
			if _, ok := h.Clients[ch]; ok {
				delete(h.Clients, ch)
				close(ch)
			}
			clientCount := len(h.Clients)
			h.Mu.Unlock()
			logger.LogPrintf("➖ 客户端离开，当前=%d", clientCount)

			if clientCount == 0 {
				h.Close()
			}

		case <-h.Closed:
			h.Mu.Lock()
			for ch := range h.Clients {
				close(ch)
			}
			h.Clients = nil
			h.Mu.Unlock()
			return
		}
	}
}

// ---------------------------
// UDP 读取循环（高并发优化）
// ---------------------------
func (h *StreamHub) readLoop() {
	for {
		select {
		case <-h.Closed:
			return
		default:
		}

		buf := h.BufPool.Get().([]byte)
		n, _, err := h.UdpConn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-h.Closed:
				return
			default:
			}
			if !errors.Is(err, net.ErrClosed) {
				logger.LogPrintf("UDP 读取错误: %v", err)
			}
			time.Sleep(time.Millisecond * 100)
			continue
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		h.BufPool.Put(buf)

		h.Mu.Lock()
		h.LastFrame = data
		clients := make([]chan []byte, 0, len(h.Clients))
		for ch := range h.Clients {
			clients = append(clients, ch)
		}
		h.Mu.Unlock()

		monitor.AddAppInboundBytes(uint64(n))
		h.broadcastToClients(clients, data)
	}
}

// ---------------------------
// 广播数据到指定客户端列表（非阻塞）
// ---------------------------
func (h *StreamHub) broadcastToClients(clients []chan []byte, data []byte) {
	for _, ch := range clients {
		select {
		case ch <- data:
		default:
		}
	}
}

// ---------------------------
// HTTP 流式接口
// ---------------------------
func (h *StreamHub) ServeHTTP(w http.ResponseWriter, r *http.Request, contentType string, updateActive func()) {
	select {
	case <-h.Closed:
		http.Error(w, "Stream hub closed", http.StatusServiceUnavailable)
		return
	default:
	}

	ch := make(chan []byte, 20)
	h.AddCh <- ch
	defer func() {
		h.RemoveCh <- ch
	}()

	w.Header().Set("Content-Type", contentType)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	for {
		select {
		case data, ok := <-ch:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			errCh := make(chan error, 1)
			go func() {
				_, err := w.Write(data)
				errCh <- err
			}()

			select {
			case err := <-errCh:
				cancel()
				if err != nil {
					if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
						logger.LogPrintf("写入客户端错误: %v", err)
					}
					return
				}
				flusher.Flush()
				if updateActive != nil {
					updateActive()
				}
			case <-writeCtx.Done():
				cancel()
				logger.LogPrintf("写入超时，关闭连接")
				return
			case <-h.Closed:
				cancel()
				logger.LogPrintf("Hub关闭，断开客户端连接")
				return
			}
		case <-ctx.Done():
			logger.LogPrintf("客户端断开连接")
			return
		case <-h.Closed:
			logger.LogPrintf("Hub关闭，断开客户端连接")
			return
		case <-time.After(60 * time.Second):
			logger.LogPrintf("客户端空闲超时，关闭连接")
			return
		}
	}
}

// ---------------------------
// 客户端迁移
// ---------------------------
func (h *StreamHub) TransferClientsTo(newHub *StreamHub) {
	h.Mu.Lock()
	defer h.Mu.Unlock()
	for ch := range h.Clients {
		newHub.Mu.Lock()
		if newHub.LastFrame != nil {
			select {
			case ch <- newHub.LastFrame:
			default:
			}
		}
		newHub.Mu.Unlock()
		newHub.AddCh <- ch
		delete(h.Clients, ch)
	}
}

// ---------------------------
// 关闭 hub
// ---------------------------
func (h *StreamHub) Close() {
	h.Mu.Lock()
	select {
	case <-h.Closed:
		h.Mu.Unlock()
		return
	default:
		close(h.Closed)
	}
	if h.UdpConn != nil {
		_ = h.UdpConn.Close()
	}
	for ch := range h.Clients {
		close(ch)
	}
	h.Clients = nil
	h.Mu.Unlock()

	HubsMu.Lock()
	for key, hub := range Hubs {
		if hub == h {
			delete(Hubs, key)
			break
		}
	}
	HubsMu.Unlock()

	logger.LogPrintf("UDP监听已关闭")
}

// ---------------------------
// Hub Key
// ---------------------------
func HubKey(addr string, ifaces []string) string {
	return addr + "|" + strings.Join(ifaces, ",")
}

// ---------------------------
// 获取或创建 hub
// ---------------------------
func GetOrCreateHub(udpAddr string, ifaces []string) (*StreamHub, error) {
	key := HubKey(udpAddr, ifaces)

	HubsMu.Lock()
	if hub, ok := Hubs[key]; ok {
		select {
		case <-hub.Closed:
			delete(Hubs, key)
		default:
			HubsMu.Unlock()
			return hub, nil
		}
	}
	HubsMu.Unlock()

	hub, err := NewStreamHub(udpAddr, ifaces)
	if err != nil {
		return nil, err
	}

	HubsMu.Lock()
	Hubs[key] = hub
	HubsMu.Unlock()
	return hub, nil
}
