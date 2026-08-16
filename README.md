# transferthing

A fast, minimal CLI for transferring files and folders over the local network.

Automatically discovers the receiver on the LAN via UDP broadcast — no need to type IP addresses.  
Supports sending **multiple files and folders in a single session**.

## Install

```bash
go build -o tt .
```

## Usage

```
transferthing send <file|folder> [more files/folders...] [-ip IP] [-port PORT]
transferthing recv [-file OUTPUT] [-ip IP] [-port PORT]
transferthing <file|folder> [more files/folders...]   # implicit send shortcut
```

### Receive

Start the receiver first. It announces itself on the network and waits.

```bash
./tt recv
```

| Flag | Default | Description |
|------|---------|-------------|
| `-file` | original name | Output path. For a single file: the filename. For multiple files: the base directory. |
| `-ip` | broadcast | Sender's IP to accept from (skips UDP discovery). |
| `-port` | `4242` | TCP/UDP port. |

### Send

```bash
# Single file
./tt send photo.jpg

# Multiple files and a folder in one go
./tt send report.pdf notes/ screenshot.png

# Implicit shortcut (no subcommand needed)
./tt report.pdf notes/ screenshot.png

# Skip auto-discovery and send to a known IP
./tt send data.zip -ip 192.168.1.42
```

| Flag | Default | Description |
|------|---------|-------------|
| `-ip` | auto-discover | Receiver's IP (bypasses UDP broadcast). |
| `-port` | `4242` | TCP/UDP port. |

## How it works

```
Receiver                           Sender
--------                           ------
ListenTCP(:4242)
SendUDP(broadcast, "TRANSFERTHING_DISCOVERY")
                                   RecvUDP → got receiver IP
                                   DialTCP(receiver:4242)
                                   Encode TransferHeader{Count: N}
                                   for each path:
                                     Encode FileMetadata{Name, Size, WireSize, IsDir}
                                     stream bytes (WireSize)
Decode header → loop N times
  Decode FileMetadata
  io.CopyN(WireSize bytes)
  if folder: unzip to disk
```

**Folders** are zipped on the sender before transfer so `WireSize` (compressed) is known upfront.  
The receiver uses `io.CopyN(WireSize)` per item — this keeps file boundaries intact across a multi-file session.

## Security

- **Zip-slip protection**: every zip entry path is validated to remain inside the output directory before extraction.
- **IP validation**: `net.ParseIP` is checked for `nil` on both sides before use.

## Features

- **Multi-file / folder transfers** — send any mix of files and folders in one command
- **Auto-discovery** — UDP broadcast finds the receiver automatically on the LAN
- **Compact progress bar** — 22-char Unicode block bar (`█▌░`) with speed and ETA, aligned across files
- **Local interface logging** — both sides log which network interface the OS chose
- **Structured logs** — via [charmbracelet/log](https://github.com/charmbracelet/log)
- **Proper error handling** — all errors propagated with context, no panics

## Dependencies

| Package | Purpose |
|---------|---------|
| [`charmbracelet/log`](https://github.com/charmbracelet/log) | Structured, coloured logging |
| [`schollz/progressbar`](https://github.com/schollz/progressbar) | Terminal progress bar |
