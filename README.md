# mc-wake-proxy

Markdown
# Minecraft Wake-on-Connect Proxy (Crafty API v2)

A lightweight, high-performance Go-based TCP proxy designed for Minecraft servers managed via **Crafty Controller v4 (API v2)**. 

This proxy listens on public Minecraft ports (e.g., `25565`) while your actual Minecraft servers run on alternative backend ports. It intercepts incoming client traffic, intelligently filters out background server list "refreshes" (Pings), and only wakes up sleeping servers via the Crafty API when a player explicitly attempts to **Join** (Login).

## Features
* **Minecraft Packet Inspection:** Decodes the initial packet handshake to distinguish between a Server List Refresh and a genuine Join attempt. Saves API requests and avoids false wakeups.
* **Multi-Server Support:** Manages multiple servers simultaneously on separate ports using lightweight Go routines.
* **Concurrent Lock Management:** Uses atomic mutexes to ensure multiple players joining simultaneously only trigger a single API call to Crafty.
* **Zero Dependencies:** Compiles into a completely standalone binary using only Go standard libraries.

---

## Setup Instructions

### 1. Prerequisites
Ensure you have Go installed on your machine or Proxmox LXC container:
```bash
sudo apt update
sudo apt install -y golang
2. Clone and Configure
Clone this repository (or copy the files) into your directory of choice:

Bash
mkdir -p /opt/mc-proxy
cd /opt/mc-proxy
Open main.go and update the servers slice configuration with your respective details:

Go
var servers = []*ServerConfig{
	{
		Name:        "Survival",
		ListenAddr:  "0.0.0.0:25565",   // Port players connect to
		BackendAddr: "127.0.0.1:25566", // Actual Minecraft server port
		CraftyURL:   "[https://127.0.0.1:8443/api/v2/servers/YOUR_SERVER_UUID/action/start_server](https://127.0.0.1:8443/api/v2/servers/YOUR_SERVER_UUID/action/start_server)",
		CraftyToken: "YOUR_CRAFTY_API_TOKEN",
	},
}
Crucial Port Setup: Ensure your servers inside Crafty are assigned to your designated BackendAddr ports (e.g., 25566) and not your public listener ports (25565). If Crafty and the proxy fight for the same port, the proxy will fail to bind.

3. Build the Binary
Compile the Go script into a single executable binary:

Bash
go build -o mc-proxy main.go
Deployment as a Systemd Service (Linux)
To ensure the proxy runs continuously in the background, automatically starts on boot, and recovers from failures, set it up as a systemd background service.

Create a new service file:

Bash
sudo nano /etc/systemd/system/mc-proxy.service
Paste the following configuration:

Ini, TOML
[Unit]
Description=Minecraft Wake-on-Connect Proxy
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/mc-proxy
ExecStart=/opt/mc-proxy/mc-proxy
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
Reload systemd, start the proxy, and enable it on boot:

Bash
sudo systemctl daemon-reload
sudo systemctl start mc-proxy
sudo systemctl enable mc-proxy
Monitoring and Logs
To view live connection handling, server wakeup events, or troubleshooting logs, use journalctl:

Bash
# View live scrollable logs
sudo journalctl -u mc-proxy -f

# View the last 50 lines of logs
sudo journalctl -u mc-proxy -n 50
