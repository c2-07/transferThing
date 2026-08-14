package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
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

	fmt.Println("waiting for receiver...")

	buf := make([]byte, 1024)

	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Fatal("discovery:", err)
		}

		if string(buf[:n]) != ServicePhrase {
			continue
		}

		fmt.Println("receiver discovered:", addr.IP)
		return addr
	}
}

func announceReceiver(port int) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.IPv4bcast,
		Port: port,
	})
	if err != nil {
		log.Fatal("discovery:", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(ServicePhrase)); err != nil {
		log.Fatal("discovery:", err)
	}

	fmt.Println("discovery broadcast sent")
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

	conn, err := net.DialTCP("tcp4", &net.TCPAddr{
		IP:   receiver.IP,
		Port: receiver.Port,
	}, nil)
	if err != nil {
		log.Fatal("connect:", err)
	}
	defer conn.Close()

	fmt.Println("connected to receiver:", receiver.IP)

	file, err := os.Open(path)
	if err != nil {
		log.Fatal("open:", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		log.Fatal("stat:", err)
	}

	err = gob.NewEncoder(conn).Encode(FileMetadata{
		Name: info.Name(),
		Size: info.Size(),
	})
	if err != nil {
		log.Fatal("metadata:", err)
	}

	_, err = io.Copy(conn, file)
	if err != nil {
		log.Fatal("transfer:", err)
	}

	fmt.Println("file sent:", info.Name())
}

func receiveFile(output string, port int) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{
		IP:   net.IPv4zero,
		Port: port,
	})
	if err != nil {
		log.Fatal("listen:", err)
	}
	defer listener.Close()

	// Start listening BEFORE announcing ourselves.
	announceReceiver(port)

	fmt.Println("waiting for sender...")

	conn, err := listener.AcceptTCP()
	if err != nil {
		log.Fatal("accept:", err)
	}
	defer conn.Close()

	fmt.Println("sender connected:", conn.RemoteAddr())

	var metadata FileMetadata

	err = gob.NewDecoder(conn).Decode(&metadata)
	if err != nil {
		log.Fatal("metadata:", err)
	}

	fmt.Println("file:", metadata.Name)
	fmt.Println("size:", metadata.Size)

	// If no output filename was supplied, use the original filename.
	if output == "" {
		output = metadata.Name
	}

	file, err := os.Create(output)
	if err != nil {
		log.Fatal("create:", err)
	}
	defer file.Close()

	_, err = io.CopyN(file, conn, metadata.Size)
	if err != nil {
		log.Fatal("transfer:", err)
	}

	fmt.Println("file received:", output)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: transferthing <send|recv>")
		return
	}

	switch os.Args[1] {

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
			log.Fatal("path:", err)
		}

		sendFile(path, *ip, *port)

	case "recv":
		recv := flag.NewFlagSet("recv", flag.ExitOnError)

		file := recv.String(
			"file",
			"",
			"output filename",
		)

		port := recv.Int(
			"port",
			DefaultPort,
			"TCP/UDP port",
		)

		recv.Parse(os.Args[2:])

		receiveFile(*file, *port)

	default:
		fmt.Println("unknown command:", os.Args[1])
	}
}
