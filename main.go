package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
)

const (
	DefaultPort   = 4242
	ServicePhrase = "TRANSFERTHING_DISCOVERY"
)

// waitForReceiver waits for a discovery broadcast from a receiver
// and returns the receiver's UDP address.
func waitForReceiver() *net.UDPAddr {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: DefaultPort,
	})
	if err != nil {
		log.Fatal(err)
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

		fmt.Println("receiver discovered:", addr)
		return addr
	}
}

// broadcastDiscovery announces that this machine is ready to receive a file.
func broadcastDiscovery() {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.IPv4bcast,
		Port: DefaultPort,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(ServicePhrase)); err != nil {
		log.Fatal("discovery:", err)
	}

	fmt.Println("discovery broadcast sent")
}

// sendFile discovers the receiver and establishes a TCP connection to it.
func sendFile() {
	receiverAddr := waitForReceiver()

	conn, err := net.DialTCP("tcp4", &net.TCPAddr{
		IP:   receiverAddr.IP,
		Port: DefaultPort,
	}, nil)
	if err != nil {
		log.Fatal("send:", err)
	}
	defer conn.Close()

	fmt.Println("connected to receiver:", receiverAddr.IP)
}

// receiveFile announces this machine and waits for the sender's TCP connection.
func receiveFile() {
	broadcastDiscovery()

	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{
		IP:   net.IPv4zero,
		Port: DefaultPort,
	})
	if err != nil {
		log.Fatal("receive:", err)
	}
	defer listener.Close()

	fmt.Println("waiting for sender...")

	conn, err := listener.AcceptTCP()
	if err != nil {
		log.Fatal("receive:", err)
	}
	defer conn.Close()

	fmt.Println("sender connected:", conn.RemoteAddr())
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: transferthing <send|recv>")
		return
	}

	command := os.Args[1]

	switch command {
	case "send":
		send := flag.NewFlagSet("send", flag.ExitOnError)
		port := send.Int("port", DefaultPort, "sender's port")
		send.Parse(os.Args[2:])

		fmt.Println("port:", *port)
		sendFile()

	case "recv":
		recv := flag.NewFlagSet("recv", flag.ExitOnError)
		port := recv.Int("port", DefaultPort, "receiver's port")
		recv.Parse(os.Args[2:])

		fmt.Println("port:", *port)
		receiveFile()

	default:
		fmt.Println("unknown command:", command)
	}
}
