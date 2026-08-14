package main

import (
	"archive/zip"
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
	Name  string
	Size  int64
	IsDir bool
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

	info, err := os.Stat(path)
	if err != nil {
		log.Fatal("stat", "err", err)
	}

	if info.IsDir() {
		sendFolder(conn, path, info)
	} else {
		sendSingleFile(conn, path, info)
	}
}

func sendSingleFile(conn net.Conn, path string, info os.FileInfo) {
	file, err := os.Open(path)
	if err != nil {
		log.Fatal("open", "err", err)
	}
	defer file.Close()

	err = gob.NewEncoder(conn).Encode(FileMetadata{
		Name:  info.Name(),
		Size:  info.Size(),
		IsDir: false,
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

// dirSize returns the total size of all files under root.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// zipDir walks srcDir and writes a zip archive into w.
func zipDir(w io.Writer, srcDir string) error {
	zw := zip.NewWriter(w)

	base := filepath.Dir(srcDir) // preserve top-level folder name inside zip

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Build the relative path inside the zip.
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		// Use forward slashes in the zip regardless of OS.
		rel = filepath.ToSlash(rel)

		if info.IsDir() {
			// Ensure directory entries end with "/".
			_, err = zw.Create(rel + "/")
			return err
		}

		fw, err := zw.Create(rel)
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		return err
	}

	return zw.Close()
}

func sendFolder(conn net.Conn, path string, info os.FileInfo) {
	approxSize, err := dirSize(path)
	if err != nil {
		log.Fatal("dirsize", "err", err)
	}

	// Send metadata. We don't know the compressed size up-front, so use the
	// uncompressed total as an approximation for the progress bar.
	err = gob.NewEncoder(conn).Encode(FileMetadata{
		Name:  info.Name(),
		Size:  approxSize,
		IsDir: true,
	})
	if err != nil {
		log.Fatal("metadata", "err", err)
	}

	bar := progressbar.DefaultBytes(approxSize, "sending")

	// Zip the directory directly into the TCP connection, tee-ing through bar.
	if err := zipDir(io.MultiWriter(conn, bar), path); err != nil {
		log.Fatal("zip", "err", err)
	}

	log.Info("folder sent", "name", info.Name())
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

	log.Info("incoming", "name", metadata.Name, "size", metadata.Size, "isDir", metadata.IsDir)

	if metadata.IsDir {
		receiveFolder(bufReader, output, metadata)
	} else {
		receiveSingleFile(bufReader, output, metadata)
	}
}

func receiveSingleFile(r io.Reader, output string, metadata FileMetadata) {
	// If no output filename was supplied, use the original filename.
	if output == "" {
		output = metadata.Name
	}

	file, err := os.Create(output)
	if err != nil {
		log.Fatal("create", "err", err)
	}
	defer file.Close()

	bar := progressbar.DefaultBytes(metadata.Size, "receiving")

	_, err = io.CopyN(io.MultiWriter(file, bar), r, metadata.Size)
	if err != nil {
		log.Fatal("transfer", "err", err)
	}

	log.Info("file received", "name", output)
}

func receiveFolder(r io.Reader, output string, metadata FileMetadata) {
	if output == "" {
		output = metadata.Name
	}

	bar := progressbar.DefaultBytes(metadata.Size, "receiving")

	// Buffer all zip data into a temp file so zip.NewReader can seek.
	tmp, err := os.CreateTemp("", "transferthing-*.zip")
	if err != nil {
		log.Fatal("tempfile", "err", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	_, err = io.Copy(io.MultiWriter(tmp, bar), r)
	if err != nil {
		log.Fatal("transfer", "err", err)
	}

	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		log.Fatal("seek", "err", err)
	}

	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		log.Fatal("unzip", "err", err)
	}

	for _, f := range zr.File {
		destPath := filepath.Join(output, filepath.FromSlash(f.Name))

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, 0755); err != nil {
				log.Fatal("mkdir", "err", err)
			}
			continue
		}

		// Ensure parent dirs exist.
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			log.Fatal("mkdir", "err", err)
		}

		out, err := os.Create(destPath)
		if err != nil {
			log.Fatal("create", "err", err)
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			log.Fatal("zip open", "err", err)
		}

		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			log.Fatal("extract", "err", err)
		}

		rc.Close()
		out.Close()
	}

	log.Info("folder received", "name", output)
}

func printUsage() {
	fmt.Println(`transferthing - A simple file transfer tool

Usage:
  transferthing <command> [arguments]
  transferthing <path> [arguments]  (shortcut for send)

Commands:
  send    Send a file or folder
  recv    Receive a file or folder

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
			log.Fatal("usage: transferthing send <file|folder> [-ip IP] [-port PORT]")
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
			"output filename or folder name",
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
		// Check if it's a file or folder, if so, assume "send"
		_, err := os.Stat(os.Args[1])
		if err == nil {
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

