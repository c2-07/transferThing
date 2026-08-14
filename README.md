# transferthing

A simple, fast, and colorful command-line utility for transferring files over the local network. 

`transferthing` automatically discovers receivers on the local network using UDP broadcast, removing the need to manually type IP addresses in most cases!

## Installation

```bash
go build -o transferthing
```

## Usage

```
transferthing - A simple file transfer tool

Usage:
  transferthing <command> [arguments]

Commands:
  send    Send a file
  recv    Receive a file

Run 'transferthing <command> -h' for more details.
```

### Receiving a file

On the receiving machine, simply run:

```bash
./transferthing recv
```

Options:
- `-file string`: specify an output filename (defaults to the original filename)
- `-port int`: specify a custom TCP/UDP port (default 4242)

### Sending a file

On the sending machine, run:

```bash
./transferthing send path/to/your/file.txt
```

Options:
- `-ip string`: bypass discovery and send to a specific IP address
- `-port int`: specify a custom receiver port (default 4242)

## Features
- **Auto-Discovery**: No need to manually type IP addresses; senders find receivers automatically on the local network.
- **Progress Bar**: See transfer speed, time remaining, and bytes transferred.
- **Colorful Logging**: Beautiful structured logs.
- **Simple**: Written in Go with minimal dependencies.
