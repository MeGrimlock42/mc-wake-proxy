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
	ListenAddr  string // Full binding address (IP:Port)
	ListenPort  string // Plain port number required for LAN broadcasting
	BackendAddr string
	CraftyURL   string
	CraftyToken string

	mu       sync.Mutex
	isWaking bool
}

// Define your servers here
var servers = []*ServerConfig{
	{
		Name:        "FunVille (Fabric 1.21.1)",
		ListenAddr:  "192.168.2.91:25565",
		ListenPort:  "25565",
		BackendAddr: "192.168.2.91:25566",
		CraftyURL:   "https://192.168.2.91:8443/api/v2/servers/53d5f157-862b-4986-998c-e4db59780f6e/action/start_server",
		CraftyToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyX2lkIjoxLCJpYXQiOjE3Nzk4NTk0MzUsInRva2VuX2lkIjoxfQ.rsC7eQittEwrXtljxK-Fl0PovjiFFt4Fsi5j8qWvnbM",
	},
	{
		Name:        "Caitlin's Place (Forge 1.20.1)",
		ListenAddr:  "192.168.2.91:25567",
		ListenPort:  "25567",
		BackendAddr: "192.168.2.91:25568",
		CraftyURL:   "https://192.168.2.91:8443/api/v2/servers/0eef9b96-0eb2-4287-958d-d7cedaa59d3c/action/start_server",
		CraftyToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyX2lkIjoxLCJpYXQiOjE3Nzk4NTk0MzUsInRva2VuX2lkIjoxfQ.rsC7eQittEwrXtljxK-Fl0PovjiFFt4Fsi5j8qWvnbM",
	},
}

func main() {
	var wg sync.WaitGroup

	for _, config := range servers {
		// 1. Start the TCP Proxy Listener
		wg.Add(1)
		go func(cfg *ServerConfig) {
			defer wg.Done()
			startProxy(cfg)
		}(config)

		// 2. Start the LAN Auto-Discovery Broadcast background thread
		go startLANBroadcast(config)
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

// Background loop that broadcasts server metadata via UDP Multicast
func startLANBroadcast(cfg *ServerConfig) {
	// Standard Minecraft LAN discovery endpoint
	multicastAddr := "224.0.2.60:4445"
	
	addr, err := net.ResolveUDPAddr("udp", multicastAddr)
	if err != nil {
		log.Printf("[%s] LAN Broadcast configuration error: %v", cfg.Name, err)
		return
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Printf("[%s] LAN Broadcast connectivity error: %v", cfg.Name, err)
		return
	}
	defer conn.Close()

	// Packet formatting rule: [MOTD]DisplayName[/MOTD][AD]Port[/AD]
	payload := fmt.Sprintf("[MOTD]%s[/MOTD][AD]%s[/AD]", cfg.Name, cfg.ListenPort)
	packetData := []byte(payload)

	log.Printf("[%s] LAN Auto-Discovery active (Broadcasting as '%s' on port %s)", cfg.Name, cfg.Name, cfg.ListenPort)

	for {
		_, err := conn.Write(packetData)
		if err != nil {
			log.Printf("[%s] LAN Broadcast packet dropped: %v", cfg.Name, err)
		}
		// Pulse every 1.5 seconds to keep the game client list populated
		time.Sleep(1500 * time.Millisecond)
	}
}

func handleConnection(clientConn net.Conn, cfg *ServerConfig) {
	defer clientConn.Close()
	clientAddr := clientConn.RemoteAddr().String()

	buffer := make([]byte, 4096)
	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := clientConn.Read(buffer)
	clientConn.SetReadDeadline(time.Time{}) 

	if err != nil || n == 0 {
		return 
	}
	initialData := buffer[:n]

	isLogin := false
	reader := bytes.NewReader(initialData)

	_, err = readVarInt(reader) 
	if err == nil {
		pktID, err := readVarInt(reader) 
		if err == nil && pktID == 0x00 { 
			_, _ = readVarInt(reader) 

			strLen, err := readVarInt(reader) 
			if err == nil {
				reader.Seek(int64(strLen), io.SeekCurrent) 

				var port uint16
				err = binary.Read(reader, binary.BigEndian, &port) 
				if err == nil {
					nextState, err := readVarInt(reader) 
					if err == nil && nextState == 2 {
						isLogin = true
					}
				}
			}
		}
	}

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
		backendConn, err = net.DialTimeout("tcp", cfg.BackendAddr, 1*time.Second)
		if err != nil {
			return 
		}
	}

	defer backendConn.Close()

	_, err = backendConn.Write(initialData)
	if err != nil {
		return
	}

	go io.Copy(backendConn, clientConn)
	io.Copy(clientConn, backendConn)
}

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
