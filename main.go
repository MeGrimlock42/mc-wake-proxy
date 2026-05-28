package main

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// --- CONFIGURATION ---
const (
	CheckInterval = 2 * time.Second  // How often to check if MC server is up
	MaxWaitTime   = 60 * time.Second // Max time to hold client before giving up
)

type ServerConfig struct {
	Name        string
	ListenAddr  string
	BackendAddr string
	CraftyURL   string
	CraftyToken string

	mu       sync.Mutex
	isWaking bool
}

// Define your servers here
var servers = []*ServerConfig{
	{
		Name:        "test",
		ListenAddr:  "192.168.2.91:25565",
		BackendAddr: "192.168.2.91:25566",
		CraftyURL:   "https://192.168.2.91:8443/api/v2/servers/e1489232-c40c-4806-986d-37c54702f54b/action/start_server",
		CraftyToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyX2lkIjoxLCJpYXQiOjE3Nzk4NTk0MzUsInRva2VuX2lkIjoxfQ.rsC7eQittEwrXtljxK-Fl0PovjiFFt4Fsi5j8qWvnbM",
	},
	{
		Name:        "Creative",
		ListenAddr:  "192.168.2.91:25567",
		BackendAddr: "192.168.2.91:25568",
		CraftyURL:   "https://192.168.2.91:8443/api/v2/servers/ff6f4182-6146-4ec0-9f21-8ffb35e08522/action/start_server",
		CraftyToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyX2lkIjoxLCJpYXQiOjE3Nzk4NTk0MzUsInRva2VuX2lkIjoxfQ.rsC7eQittEwrXtljxK-Fl0PovjiFFt4Fsi5j8qWvnbM",
	},
}

func main() {
	var wg sync.WaitGroup

	for _, config := range servers {
		wg.Add(1)
		go func(cfg *ServerConfig) {
			defer wg.Done()
			startProxy(cfg)
		}(config)
	}

	wg.Wait()
}

func startProxy(cfg *ServerConfig) {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Fatalf("[%s] Failed to bind to %s: %v", cfg.Name, cfg.ListenAddr, err)
	}
	log.Printf("[%s] Proxy listening on %s -> Forwarding to %s", cfg.Name, cfg.ListenAddr, cfg.BackendAddr)

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("[%s] Failed to accept connection: %v", cfg.Name, err)
			continue
		}
		go handleConnection(clientConn, cfg)
	}
}

func handleConnection(clientConn net.Conn, cfg *ServerConfig) {
	defer clientConn.Close()
	clientAddr := clientConn.RemoteAddr().String()

	// 1. Read the initial Minecraft payload (The Handshake)
	buffer := make([]byte, 4096)
	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := clientConn.Read(buffer)
	clientConn.SetReadDeadline(time.Time{}) // Reset deadline

	if err != nil || n == 0 {
		return // Client disconnected or timed out
	}
	initialData := buffer[:n]

	// 2. Parse the Handshake to determine if it's a Join or a Ping
	isLogin := false
	reader := bytes.NewReader(initialData)

	_, err = readVarInt(reader) // Packet Length
	if err == nil {
		pktID, err := readVarInt(reader) // Packet ID
		if err == nil && pktID == 0x00 { // 0x00 is Handshake
			_, _ = readVarInt(reader) // Protocol Version

			strLen, err := readVarInt(reader) // Server Address Length
			if err == nil {
				reader.Seek(int64(strLen), io.SeekCurrent) // Skip Address string

				var port uint16
				err = binary.Read(reader, binary.BigEndian, &port) // Server Port
				if err == nil {
					nextState, err := readVarInt(reader) // Next State (1=Ping, 2=Login)
					if err == nil && nextState == 2 {
						isLogin = true
					}
				}
			}
		}
	}

	// 3. Routing Logic based on Client Intent
	var backendConn net.Conn

	if isLogin {
		log.Printf("[%s] [+] Join attempt from %s", cfg.Name, clientAddr)
		backendConn, err = net.DialTimeout("tcp", cfg.BackendAddr, 2*time.Second)

		if err != nil {
			log.Printf("[%s] [-] Backend is down. Waking server...", cfg.Name)
			wakeServer(cfg)

			backendConn = waitForBackend(cfg)
			if backendConn == nil {
				log.Printf("[%s] [!] Server failed to start for %s", cfg.Name, clientAddr)
				return
			}
		}
	} else {
		// It's a Server List Ping (Refresh)
		backendConn, err = net.DialTimeout("tcp", cfg.BackendAddr, 1*time.Second)
		if err != nil {
			// Backend is asleep. Do NOT wake it. 
			// Drop connection so the server list shows it as offline/sleeping.
			return
		}
	}

	defer backendConn.Close()

	// 4. Send the intercepted Handshake data to the backend first
	_, err = backendConn.Write(initialData)
	if err != nil {
		return
	}

	// 5. Pipe the rest of the connection seamlessly
	go io.Copy(backendConn, clientConn)
	io.Copy(clientConn, backendConn)
}

// Helper to decode Minecraft's custom VarInt format
func readVarInt(r *bytes.Reader) (int, error) {
	var num int
	var shift uint
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		num |= (int(b&0x7F) << shift)
		if (b & 0x80) == 0 {
			break
		}
		shift += 7
		if shift >= 32 {
			return 0, fmt.Errorf("VarInt too big")
		}
	}
	return num, nil
}

func wakeServer(cfg *ServerConfig) {
	cfg.mu.Lock()
	defer cfg.mu.Unlock()

	if cfg.isWaking {
		return
	}

	cfg.isWaking = true
	log.Printf("[%s] [*] Sending start request to Crafty API v2...", cfg.Name)

	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	req, err := http.NewRequest("POST", cfg.CraftyURL, nil)
	if err != nil {
		log.Printf("[%s] [!] Failed to create request: %v", cfg.Name, err)
		return
	}
	req.Header.Add("Authorization", "Bearer "+cfg.CraftyToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[%s] [!] Crafty API request failed: %v", cfg.Name, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("[%s] [+] Crafty API successfully triggered server start.", cfg.Name)
	} else {
		log.Printf("[%s] [!] Crafty API returned status: %d", cfg.Name, resp.StatusCode)
	}

	go func() {
		time.Sleep(MaxWaitTime)
		cfg.mu.Lock()
		cfg.isWaking = false
		cfg.mu.Unlock()
	}()
}

func waitForBackend(cfg *ServerConfig) net.Conn {
	start := time.Now()
	for time.Since(start) < MaxWaitTime {
		conn, err := net.DialTimeout("tcp", cfg.BackendAddr, 1*time.Second)
		if err == nil {
			return conn
		}
		time.Sleep(CheckInterval)
	}
	return nil
}