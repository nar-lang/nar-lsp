package nar_lsp

import (
	"bufio"
	"fmt"
	"github.com/nar-lang/nar-lsp/internal"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
)

func LanguageServer(tcpPort int, cacheDir string) error {
	if tcpPort == 0 {
		return ServeStdio(cacheDir)
	} else {
		return ServeTcp(tcpPort, cacheDir)
	}
}

func ServeStdio(cacheDir string) error {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	s := internal.NewServer(cacheDir, writeMessage(writer))
	defer s.Close()

	for {
		if msg, err := readMessage(reader); nil != err {
			return err
		} else {
			s.GotMessage(msg)
		}
	}
}

func ServeTcp(port int, cacheDir string) error {
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	ln, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	handleConnection := func(conn net.Conn) {
		defer conn.Close()
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)

		s := internal.NewServer(cacheDir, writeMessage(writer))
		defer s.Close()

		for {
			if msg, err := readMessage(reader); nil != err {
				if err == io.EOF {
					break
				}
				log.Println(err)
			} else {
				s.GotMessage(msg)
			}
		}
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handleConnection(conn)
	}
}

func writeMessage(writer *bufio.Writer) func([]byte) {
	return func(msg []byte) {
		writer.Write([]byte("Content-Length: " + strconv.Itoa(len(msg)) + "\r\n\r\n"))
		writer.Write(msg)
		writer.Flush()
	}
}

func readMessage(reader *bufio.Reader) ([]byte, error) {
	text, err := reader.ReadString('\n')
	if nil != err {
		return nil, err
	}

	parts := strings.Split(strings.TrimSpace(text), ":")
	contentLen := 0
	if parts[0] == "Content-Length" {
		contentLen, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, err
		}
	} else {
		return nil, io.EOF
	}

	for text != "\r\n" {
		text, err = reader.ReadString('\n')
		if nil != err {
			return nil, err
		}
	}

	buf := make([]byte, contentLen)
	loaded := 0
	for loaded < contentLen {
		n, err := reader.Read(buf[loaded:])
		if nil != err {
			return nil, err
		}
		loaded += n
	}
	return buf, nil
}

func Version() int {
	return internal.Version
}
