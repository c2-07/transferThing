package main

import (
	"archive/zip"
	"bufio"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/schollz/progressbar/v3"
)

const (
	DefaultPort   = 4242
	ServicePhrase = "TRANSFERTHING_DISCOVERY"
)

// TransferHeader is the first thing sent over the wire; it tells the receiver
// how many files/folders to expect in this session.
type TransferHeader struct {
	Count int
}

type FileMetadata struct {
	Name     string
	Size     int64 // original / uncompressed size (used for progress display)
	WireSize int64 // exact bytes on the wire (= Size for files, = compressed zip size for dirs)
	IsDir    bool
}

// discoverReceiver listens on the given UDP port for a receiver announcement
// and returns the sender's address.
func discoverReceiver(port int) (*net.UDPAddr, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: port,
	})
	if err != nil {
		return nil, fmt.Errorf("discovery: listen UDP: %w", err)
	}
	defer conn.Close()

	log.Info("waiting for receiver...")

	buf := make([]byte, 1024)

	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil, fmt.Errorf("discovery: read UDP: %w", err)
		}

		if string(buf[:n]) != ServicePhrase {
			continue
		}

		log.Info("receiver discovered", "ip", addr.IP)
		return addr, nil
	}
}

func announceReceiver(targetIP net.IP, port int) error {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   targetIP,
		Port: port,
	})
	if err != nil {
		return fmt.Errorf("discovery: dial UDP: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(ServicePhrase)); err != nil {
		return fmt.Errorf("discovery: write UDP: %w", err)
	}

	log.Info("discovery packet sent", "target", targetIP)
	return nil
}

// sendFiles connects to the receiver and transfers all paths over one TCP session.
func sendFiles(paths []string, ip string, port int) error {
	var receiver *net.UDPAddr

	if ip == "" {
		var err error
		receiver, err = discoverReceiver(port)
		if err != nil {
			return err
		}
	} else {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return fmt.Errorf("invalid receiver IP address: %q", ip)
		}
		receiver = &net.UDPAddr{
			IP:   parsed,
			Port: port,
		}
	}

	conn, err := net.DialTCP("tcp4", nil, &net.TCPAddr{
		IP:   receiver.IP,
		Port: port,
	})
	if err != nil {
		return fmt.Errorf("connect to receiver %s:%d: %w", receiver.IP, port, err)
	}
	defer conn.Close()

	log.Info("connected to receiver", "ip", receiver.IP)
	log.Info("using local interface", "addr", conn.LocalAddr())

	// Bug fix #1/#2: reuse a single gob encoder for the entire session.
	// Creating a fresh encoder per-call re-sends gob type descriptors every
	// time, bloating the wire and risking decoder mismatch on the other end.
	enc := gob.NewEncoder(conn)

	if err := enc.Encode(TransferHeader{Count: len(paths)}); err != nil {
		return fmt.Errorf("encode header: %w", err)
	}

	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %q: %w", path, err)
		}

		log.Info("sending", "progress", fmt.Sprintf("%d/%d", i+1, len(paths)), "name", info.Name())

		if info.IsDir() {
			if err := sendFolder(enc, conn, path, info); err != nil {
				return err
			}
		} else {
			if err := sendSingleFile(enc, conn, path, info); err != nil {
				return err
			}
		}
	}
	return nil
}

// newBar returns a compact, modern progress bar.
// The description is padded to a fixed width so multiple bars align vertically.
func newBar(total int64, desc string) *progressbar.ProgressBar {
	return progressbar.NewOptions64(
		total,
		progressbar.OptionSetDescription(fmt.Sprintf("%-12s", desc)),
		progressbar.OptionSetWidth(22),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "▌",
			SaucerPadding: "░",
			BarStart:      "",
			BarEnd:        "",
		}),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionUseANSICodes(true),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
	)
}

// finishBar ensures the completion newline is always printed, even for
// zero-byte transfers where bar.Add is never called and OnCompletion never fires.
func finishBar(bar *progressbar.ProgressBar) {
	_ = bar.Finish()
	fmt.Fprint(os.Stderr, "\n")
}

func sendSingleFile(enc *gob.Encoder, conn net.Conn, path string, info os.FileInfo) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer file.Close()

	// Bug fix #3: stat the already-open file descriptor instead of re-using the
	// pre-open os.FileInfo. The file could have changed size between the earlier
	// os.Stat call and now; using the fd-based stat ensures WireSize matches
	// what io.Copy will actually read, preventing the receiver's io.CopyN from
	// either starving (file shrank) or bleeding into the next file's metadata
	// (file grew).
	finfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat open file %q: %w", path, err)
	}
	size := finfo.Size()

	if err := enc.Encode(FileMetadata{
		Name:     info.Name(),
		Size:     size,
		WireSize: size,
		IsDir:    false,
	}); err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	bar := newBar(size, "sending")
	defer finishBar(bar) // Bug fix #9: always emit the completion newline

	if _, err := io.Copy(io.MultiWriter(conn, bar), file); err != nil {
		return fmt.Errorf("transfer %q: %w", info.Name(), err)
	}

	log.Info("file sent", "name", info.Name())
	return nil
}

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

// zipDirWithProgress walks srcDir and writes a zip archive into w, advancing
// bar by the number of uncompressed (input) bytes read from each file.
// Tracking input bytes — not compressed output — prevents the bar from
// overflowing when already-compressed files (images, videos, etc.) produce
// zip output that is larger than the raw source.
func zipDirWithProgress(w io.Writer, srcDir string, bar io.Writer) error {
	zw := zip.NewWriter(w)

	// filepath.Dir preserves the top-level folder name as an entry inside the zip
	// (e.g. zipping "photos/" keeps "photos/img.jpg" rather than just "img.jpg").
	base := filepath.Dir(srcDir)

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel) // zip spec requires forward slashes

		if info.IsDir() {
			hdr := &zip.FileHeader{
				Name:   rel + "/", // trailing slash marks a directory entry
				Method: zip.Store,
			}
			hdr.SetModTime(info.ModTime())
			_, err = zw.CreateHeader(hdr)
			return err
		}

		hdr := &zip.FileHeader{
			Name:               rel,
			Method:             zip.Deflate,
			UncompressedSize64: uint64(info.Size()),
		}
		hdr.SetModTime(info.ModTime())
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}

		// Bug fix #5: open, copy, and close each file explicitly instead of
		// using defer inside the Walk closure. defer in a closure only runs when
		// the enclosing *function* returns (i.e. after the entire Walk), so every
		// file descriptor stays open until the whole directory has been zipped.
		// For large directories this exhausts the OS fd limit.
		f, err := os.Open(path)
		if err != nil {
			return err
		}

		// Copy through the progress bar on the READ side so we count input
		// bytes, not potentially-larger compressed output bytes.
		_, copyErr := io.Copy(fw, io.TeeReader(f, bar))
		f.Close() // always close immediately, regardless of copy error
		return copyErr
	})
	if err != nil {
		return err
	}

	return zw.Close()
}

// sendFolder zips the directory into a temp file (to learn the compressed size),
// sends metadata with the exact wire size, then streams the zip.
func sendFolder(enc *gob.Encoder, conn net.Conn, path string, info os.FileInfo) error {
	uncompressedSize, err := dirSize(path)
	if err != nil {
		return fmt.Errorf("compute dir size: %w", err)
	}

	// Buffer the zip locally so we know the exact compressed byte count before
	// sending metadata. This is required for multi-file transfers: the receiver
	// must know exactly how many bytes to read (io.CopyN) so it doesn't eat
	// the next file's metadata.
	tmp, err := os.CreateTemp("", "transferthing-send-*.zip")
	if err != nil {
		return fmt.Errorf("create send temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	compressBar := newBar(uncompressedSize, "compressing")
	defer finishBar(compressBar) // Bug fix #9: always emit completion newline
	if err := zipDirWithProgress(tmp, path, compressBar); err != nil {
		return fmt.Errorf("zip %q: %w", info.Name(), err)
	}

	compressedSize, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("seek send temp file: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind send temp file: %w", err)
	}

	if err := enc.Encode(FileMetadata{
		Name:     info.Name(),
		Size:     uncompressedSize,
		WireSize: compressedSize,
		IsDir:    true,
	}); err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	sendBar := newBar(compressedSize, "sending")
	defer finishBar(sendBar) // Bug fix #9
	if _, err := io.Copy(io.MultiWriter(conn, sendBar), tmp); err != nil {
		return fmt.Errorf("stream zip %q: %w", info.Name(), err)
	}

	log.Info("folder sent", "name", info.Name())
	return nil
}

// receiveFiles listens for a TCP connection, reads a TransferHeader, then
// receives each file/folder in sequence.
func receiveFiles(output, ip string, port int) error {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{
		IP:   net.IPv4zero,
		Port: port,
	})
	if err != nil {
		return fmt.Errorf("listen on :%d: %w", port, err)
	}
	defer listener.Close()

	targetIP := net.IPv4bcast
	if ip != "" {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return fmt.Errorf("invalid sender IP address: %q", ip)
		}
		targetIP = parsed
	}

	// Listen before announcing: avoids a race where the sender connects before we're ready.
	if err := announceReceiver(targetIP, port); err != nil {
		return err
	}

	log.Info("waiting for sender...")

	conn, err := listener.AcceptTCP()
	if err != nil {
		return fmt.Errorf("accept connection: %w", err)
	}
	defer conn.Close()

	log.Info("sender connected", "addr", conn.RemoteAddr())
	log.Info("using local interface", "addr", conn.LocalAddr())

	// gob internally buffers reads from the connection, which can consume
	// bytes belonging to the file payload. We must share a single bufio.Reader
	// across the entire session so those bytes aren't lost between files.
	bufReader := bufio.NewReader(conn)

	// Bug fix #1/#2: reuse a single gob decoder for the entire session.
	// Each gob.NewDecoder wraps the reader in its own internal buffer, so
	// creating a new decoder per message silently discards any bytes that the
	// previous decoder read ahead. A single shared decoder avoids this.
	dec := gob.NewDecoder(bufReader)

	var header TransferHeader
	if err := dec.Decode(&header); err != nil {
		return fmt.Errorf("decode header: %w", err)
	}

	log.Info("incoming transfer", "count", header.Count)

	for i := 0; i < header.Count; i++ {
		var metadata FileMetadata
		if err := dec.Decode(&metadata); err != nil {
			return fmt.Errorf("decode metadata [%d/%d]: %w", i+1, header.Count, err)
		}

		log.Info("incoming",
			"file", fmt.Sprintf("%d/%d", i+1, header.Count),
			"name", metadata.Name,
			"size", metadata.Size,
			"isDir", metadata.IsDir,
		)

		// Bug fix #6: sanitize the metadata.Name from the wire before using it
		// as a path component. A malicious sender could set Name to "../evil"
		// to write outside the intended destination directory.
		safeName := filepath.Base(metadata.Name)
		if safeName == "." || safeName == ".." || safeName == "" {
			return fmt.Errorf("received unsafe file name %q", metadata.Name)
		}

		// Determine where to write this item:
		//   - single file transfer + -file flag → use flag value as the exact output path
		//   - multi-file transfer + -file flag  → treat flag as a base directory
		//   - no -file flag                     → use original name in current dir
		var outPath string
		switch {
		case output != "" && header.Count == 1:
			outPath = output
		case output != "":
			outPath = filepath.Join(output, safeName)
		default:
			outPath = safeName
		}

		if metadata.IsDir {
			if err := receiveFolder(bufReader, outPath, metadata); err != nil {
				return err
			}
		} else {
			if err := receiveSingleFile(bufReader, outPath, metadata); err != nil {
				return err
			}
		}
	}
	return nil
}

func receiveSingleFile(r io.Reader, output string, metadata FileMetadata) error {
	if output == "" {
		output = metadata.Name
	}

	file, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("create %q: %w", output, err)
	}
	defer file.Close()

	bar := newBar(metadata.WireSize, "receiving")
	defer finishBar(bar) // Bug fix #9

	if _, err := io.CopyN(io.MultiWriter(file, bar), r, metadata.WireSize); err != nil {
		return fmt.Errorf("receive %q: %w", metadata.Name, err)
	}

	log.Info("file received", "name", output)
	return nil
}

// sanitizePath guards against zip-slip: a crafted archive entry like "../../etc/passwd"
// would escape the destination directory without this check.
func sanitizePath(baseDir, entryName string) (string, error) {
	dest := filepath.Join(baseDir, filepath.FromSlash(entryName))

	// Bug fix #10: filepath.Clean("/") == "/" so naively appending
	// os.PathSeparator gives "//", which never matches the cleaned dest prefix.
	// Use a separator-terminated clean path only when baseDir is not already the root.
	cleanBase := filepath.Clean(baseDir)
	if !strings.HasSuffix(cleanBase, string(os.PathSeparator)) {
		cleanBase += string(os.PathSeparator)
	}

	if !strings.HasPrefix(filepath.Clean(dest)+string(os.PathSeparator), cleanBase) {
		return "", fmt.Errorf("zip-slip: illegal path %q escapes destination %q", entryName, baseDir)
	}
	return dest, nil
}

func receiveFolder(r io.Reader, output string, metadata FileMetadata) error {
	if output == "" {
		output = metadata.Name
	}

	bar := newBar(metadata.WireSize, "receiving")
	defer finishBar(bar) // Bug fix #9

	// zip.NewReader requires io.ReaderAt (i.e. seek), so we buffer to a temp file first.
	tmp, err := os.CreateTemp("", "transferthing-*.zip")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if _, err := io.CopyN(io.MultiWriter(tmp, bar), r, metadata.WireSize); err != nil {
		return fmt.Errorf("receive zip data: %w", err)
	}

	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("seek temp file: %w", err)
	}

	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	for _, f := range zr.File {
		destPath, err := sanitizePath(output, f.Name)
		if err != nil {
			return err
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, 0755); err != nil {
				return fmt.Errorf("mkdir %q: %w", destPath, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("mkdir %q: %w", filepath.Dir(destPath), err)
		}

		if err := extractZipEntry(f, destPath); err != nil {
			return err
		}
	}

	log.Info("folder received", "name", output)
	return nil
}

func extractZipEntry(f *zip.File, destPath string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %q: %w", f.Name, err)
	}
	defer rc.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create %q: %w", destPath, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("extract %q: %w", f.Name, err)
	}
	return nil
}

func printUsage() {
	fmt.Println(`transferthing - A simple file transfer tool

Usage:
  transferthing send <file|folder> [more files/folders...] [-ip IP] [-port PORT]
  transferthing recv [-file OUTPUT] [-ip IP] [-port PORT]
  transferthing <file|folder> [more files/folders...] [-ip IP] [-port PORT]

Commands:
  send    Send one or more files/folders
  recv    Receive files/folders

Run 'transferthing <command> -h' for more details.`)
}

func main() {
	if err := run(); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		printUsage()
		return nil
	}

	switch os.Args[1] {

	case "help", "-h", "--help":
		printUsage()
		return nil

	case "send":
		send := flag.NewFlagSet("send", flag.ExitOnError)
		ip := send.String("ip", "", "receiver IP")
		port := send.Int("port", DefaultPort, "receiver port")
		send.Parse(os.Args[2:])

		args := send.Args()
		if len(args) == 0 {
			return errors.New("usage: transferthing send <file|folder> [more...] [-ip IP] [-port PORT]")
		}

		paths, err := absPaths(args)
		if err != nil {
			return err
		}

		return sendFiles(paths, *ip, *port)

	case "recv":
		recv := flag.NewFlagSet("recv", flag.ExitOnError)
		file := recv.String("file", "", "output path (filename for single file, directory for multi-file)")
		ip := recv.String("ip", "", "sender IP (instead of UDP broadcast)")
		port := recv.Int("port", DefaultPort, "TCP/UDP port")
		recv.Parse(os.Args[2:])

		return receiveFiles(*file, *ip, *port)

	default:
		// Bare path(s) as first arg → implicit "send" shortcut.
		if _, err := os.Stat(os.Args[1]); err == nil {
			send := flag.NewFlagSet("send", flag.ExitOnError)
			ip := send.String("ip", "", "receiver IP")
			port := send.Int("port", DefaultPort, "receiver port")
			send.Parse(os.Args[2:])

			rawPaths := append([]string{os.Args[1]}, send.Args()...)
			paths, err := absPaths(rawPaths)
			if err != nil {
				return err
			}

			return sendFiles(paths, *ip, *port)
		}

		printUsage()
		return fmt.Errorf("unknown command: %q", os.Args[1])
	}
}

// absPaths resolves each raw path to an absolute path.
func absPaths(raw []string) ([]string, error) {
	out := make([]string, len(raw))
	for i, r := range raw {
		p, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", r, err)
		}
		out[i] = p
	}
	return out, nil
}
