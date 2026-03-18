# DNS Multiplexer — High-Performance DNS Tunneling & Proxy

A robust, latency-aware DNS multiplexing service designed to bypass aggressive network censorship (like those in Iran) by intelligently routing traffic through a pool of verified domestic DNS resolvers.

## 🚀 Overview

The DNS Multiplexer acts as a "Smart Bridge" between your restricted network and a remote VPN server (DNSTT/NoizeDNS). Instead of relying on a single DNS resolver that can be easily blocked or throttled, this service constantly scans thousands of Iranian DNS servers to find the fastest, most reliable "holes" in the firewall.

## ✨ Key Features

- **Smart Latency-Based Routing**: Automatically tracks the performance of every DNS query and prioritizes the fastest resolvers in real-time.
- **`findns` Integration**: Uses the professional `findns` engine for **End-to-End (E2E) Verification**. It doesn't just check if a DNS is "up"—it verifies that a SOCKS5 tunnel can actually be established through it.
- **Proactive Auto-Scanning**: Monitors your resolver pool health every 10 seconds. If the number of working resolvers drops, it triggers an emergency scan to find fresh backups before you lose connectivity.
- **Advanced Tunnel Settings**: Full support for Android-style optimizations:
  - **MTU Tuning**: Fragment packets to bypass DPI.
  - **DNS Query Size Mapping**: Limit query length to avoid detection.
  - **Stealth Mode**: Enable advanced obfuscation for NoizeDNS/Sayedns.
- **Hot-Reloading**: Update your `resolvers.txt` on the fly without restarting the service (via SIGHUP or automated file watcher).

## 🏗 Architecture & Internal Logic

### 1. How it Works (The Flow)
1.  **Bootstrap**: On startup, the `deploy.sh` script installs the `dns-multiplexer` binary. Even if your server is "blacked out," it uses your laptop's proxy during install to get everything ready.
2.  **Initial Scan**: The service starts and immediately launches `findns`. It tests your `resolvers.txt` and ~7,800+ other Iranian DNS servers.
3.  **E2E Verification**: For each server, it attempts a real SOCKS5 handshake to your Germany VPS. Only servers that succeed are added to the "Active Pool."
4.  **Multiplexing**: When you use the internet, your traffic is split across the **Top 20** fastest resolvers. This makes your traffic pattern look like random DNS noise to the ISP.
5.  **Steady State**: Every 5 minutes, it re-scans to find even faster servers. Every 10 seconds, it checks if any current servers have died.

### 2. Core Components
- **`dns-multiplexer` (The Brain)**: A high-concurrency Go engine that manages the proxy, the resolver pool, and the scanner lifecycle.
- **`findns` (The Scout)**: A specialized E2E verification tool that finds working "holes" in the firewall.
- **`slipnet` (The Runner)**: The underlying tunnel client that handles the encrypted connection to Germany.
- **`resolvers.txt` (The Map)**: A list of high-quality domestic Iranian DNS (Shecan, Electro, etc.) that we know are usually fast.

## 🛠 What is Modifiable? (Configuration)

The service is highly tunable. Most settings are managed via the Systemd service flags:

| Setting | Flag / File | Default | Why change it? |
|---------|-------------|---------|----------------|
| **Tunnel Profile** | `--tunnel-profile` | - | Your `slipnet://` config string. Required. |
| **MTU** | `--tunnel-mtu` | `512` | Lower (e.g. 128) for very restricted networks; higher (1280) for speed. |
| **Query Size** | `--tunnel-query-size`| `0` (Auto) | Set to `50` to force small DNS packets that bypass DPI. |
| **Stealth Mode** | `--tunnel-stealth` | `false` | Enable for NoizeDNS/Sayedns to use advanced obfuscation. |
| **Scan Interval**| `--scan-interval` | `5m` | Shorter (1m) if the firewall is blocking resolvers quickly. |
| **Pool Size** | `--scan-top` | `20` | More resolvers = better stealth, but slightly higher latency. |
| **Resolvers** | `resolvers.txt` | Bundled | Add your own "secret" Iranian DNS servers here for better performance. |

## 📁 File Structure

- `/etc/dns-multiplexer/`: Configuration home.
  - `resolvers.txt`: Edit this to add/remove DNS servers.
  - `profile.conf`: Stores your `slipnet://` URI securely.
### 🔍 Deep Global Scan
The system now includes an "Emergency Fallback". If the initial `resolvers.txt` fails to find any working DNS servers, it automatically triggers a **Deep Global Scan** across 7,800+ Iranian DNS servers to find working ones instantly.

### 📊 Real-Time Visibility
Monitoring the health of your tunnel is now easier:
- **`verified.txt`**: See all currently active, verified resolvers in real-time at `/etc/dns-multiplexer/verified.txt`.
- **Live Logs**: Watch the SOCKS5 handshake and tunnel performance with `sudo dns-mux --logs`.
- **Status Dashboard**: Get success rates and latencies with `sudo dns-mux --status`.

✔ /usr/local/bin/dns-mux: The management utility (symlink to `deploy.sh`).
✔ /var/log/dns-multiplexer/dns-mux.log: The source of truth for debugging.

## 🚀 Deployment Options

### Option 1: Zero-Internet / Offline Setup (Recommended for 1GB VPS)
If your server has a slow connection or loses access to Go/GitHub, use this "Portable" method:
1.  **On your laptop**: Download this repository as a ZIP file (or `git clone`).
2.  **Upload to Server**: SCP the entire folder to your VPS.
3.  **Install**: Run the script. It will find the pre-built binaries in `bin/` and install them INSTANTLY with **zero downloads**.

```bash
cd DNS-Multiplexer
sudo -E bash deploy.sh --auto --tunnel --profile "slipnet://..."
```

### Option 2: Automated Installation (Requires Internet)
```bash
# Pull and run directly
bash <(curl -Ls https://raw.githubusercontent.com/arielesfahani/DNS-Multiplexer/main/deploy.sh) \
  --auto --tunnel --profile "slipnet://..."
```

## ⚖️ License

MIT License. Built with ⚡ by the community for a free and open internet.
