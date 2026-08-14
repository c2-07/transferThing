package main

import (
	"bufio"
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"github.com/charmbracelet/log"
	"github.com/schollz/progressbar/v3"
)

const (
	DefaultPort   = 4242
	ServicePhrase = "TRANSFERTHING_DISCOVERY"
)

type FileMetadata struct {
	Name string
	Size int64
}

func discoverReceiver(port int) *net.UDPAddr {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: port,
	})
	if err != nil {
		log.Fatal("discovery:", err)
	}
	defer conn.Close()

	log.Info("waiting for receiver...")

	buf := make([]byte, 1024)

	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Fatal("discovery", "err", err)
		}

		if string(buf[:n]) != ServicePhrase {
			continue
		}

		log.Info("receiver discovered", "ip", addr.IP)
		return addr
	}
}

func announceReceiver(targetIP net.IP, port int) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   targetIP,
		Port: port,
	})
	if err != nil {
		log.Fatal("discovery", "err", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(ServicePhrase)); err != nil {
		log.Fatal("discovery", "err", err)
	}

	log.Info("discovery packet sent", "target", targetIP)
}

func sendFile(path, ip string, port int) {
	var receiver *net.UDPAddr

	if ip == "" {
		// No IP supplied → discover receiver using UDP broadcast.
		receiver = discoverReceiver(port)
	} else {
		// IP supplied → skip discovery.
		receiver = &net.UDPAddr{
			IP:   net.ParseIP(ip),
			Port: port,
		}
	}

	conn, err := net.DialTCP("tcp4", nil, &net.TCPAddr{
		IP:   receiver.IP,
		Port: port,
	})
	if err != nil {
		log.Fatal("connect", "err", err)
	}
	defer conn.Close()

	log.Info("connected to receiver", "ip", receiver.IP)

	file, err := os.Open(path)
	if err != nil {
		log.Fatal("open", "err", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		log.Fatal("stat", "err", err)
	}

	err = gob.NewEncoder(conn).Encode(FileMetadata{
		Name: info.Name(),
		Size: info.Size(),
	})
	if err != nil {
		log.Fatal("metadata", "err", err)
	}

	bar := progressbar.DefaultBytes(
		info.Size(),
		"sending",
	)

	_, err = io.Copy(io.MultiWriter(conn, bar), file)
	if err != nil {
		log.Fatal("transfer", "err", err)
	}

	log.Info("file sent", "name", info.Name())
}

func receiveFile(output, ip string, port int) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{
		IP:   net.IPv4zero,
		Port: port,
	})
	if err != nil {
		log.Fatal("listen", "err", err)
	}
	defer listener.Close()

	targetIP := net.IPv4bcast
	if ip != "" {
		targetIP = net.ParseIP(ip)
	}

	// Start listening BEFORE announcing ourselves.
	announceReceiver(targetIP, port)

	log.Info("waiting for sender...")

	conn, err := listener.AcceptTCP()
	if err != nil {
		log.Fatal("accept", "err", err)
	}
	defer conn.Close()

	log.Info("sender connected", "addr", conn.RemoteAddr())

	var metadata FileMetadata

	// gob internally buffers reads from the connection, which can consume
	// bytes belonging to the file payload. We must share a single bufio.Reader
	// between the decoder and the subsequent file copy so those bytes aren't lost.
	bufReader := bufio.NewReader(conn)
	err = gob.NewDecoder(bufReader).Decode(&metadata)
	if err != nil {
		log.Fatal("metadata", "err", err)
	}

	log.Info("incoming file", "name", metadata.Name, "size", metadata.Size)

	// If no output filename was supplied, use the original filename.
	if output == "" {
		output = metadata.Name
	}

	file, err := os.Create(output)
	if err != nil {
		log.Fatal("create", "err", err)
	}
	defer file.Close()

	bar := progressbar.DefaultBytes(
		metadata.Size,
		"receiving",
	)

	_, err = io.CopyN(io.MultiWriter(file, bar), bufReader, metadata.Size)
	if err != nil {
		log.Fatal("transfer", "err", err)
	}

	log.Info("file received", "name", output)
}

func printUsage() {
	fmt.Println(`transferthing - A simple file transfer tool

Usage:
  transferthing <command> [arguments]
  transferthing <filename> [arguments]  (shortcut for send)

Commands:
  send    Send a file
  recv    Receive a file

Run 'transferthing <command> -h' for more details.`)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	switch os.Args[1] {

	case "help", "-h", "--help":
		printUsage()
		return

	case "send":
		send := flag.NewFlagSet("send", flag.ExitOnError)

		ip := send.String("ip", "", "receiver IP")
		port := send.Int("port", DefaultPort, "receiver port")

		send.Parse(os.Args[2:])

		args := send.Args()

		if len(args) != 1 {
			log.Fatal("usage: transferthing send <file> [-ip IP] [-port PORT]")
		}

		file := args[0]

		path, err := filepath.Abs(file)
		if err != nil {
			log.Fatal("path", "err", err)
		}

		sendFile(path, *ip, *port)

	case "recv":
		recv := flag.NewFlagSet("recv", flag.ExitOnError)

		file := recv.String(
			"file",
			"",
			"output filename",
		)

		ip := recv.String(
			"ip",
			"",
			"sender IP to discover (instead of UDP broadcast)",
		)

		port := recv.Int(
			"port",
			DefaultPort,
			"TCP/UDP port",
		)

		recv.Parse(os.Args[2:])

		receiveFile(*file, *ip, *port)

	default:
		// Check if it's a file, if so, assume "send"
		info, err := os.Stat(os.Args[1])
		if err == nil && !info.IsDir() {
			send := flag.NewFlagSet("send", flag.ExitOnError)
			ip := send.String("ip", "", "receiver IP")
			port := send.Int("port", DefaultPort, "receiver port")

			send.Parse(os.Args[2:])

			path, err := filepath.Abs(os.Args[1])
			if err != nil {
				log.Fatal("path", "err", err)
			}

			sendFile(path, *ip, *port)
			return
		}

		log.Error("unknown command", "command", os.Args[1])
		printUsage()
	}
}
