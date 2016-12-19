package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/labstack/gommon/log"
	"io"
	"net"
	"strings"
)

var configMap = map[string]struct {
	Cmd   []string
	Image string
}{
	"cpp":        {[]string{"-stream=true"}, "phluent/clang"},
	"python":     {[]string{"-stream=true"}, "phluent/python"},
	"javascript": {[]string{"-stream=true"}, "phluent/javascript"},
	"typescript": {[]string{"-stream=true"}, "phluent/typescript"},
}

type Problem struct {
	Id string `json:"id"`
}

type InMemoryFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type Payload struct {
	Language string          `json:"language"`
	Files    []*InMemoryFile `json:"files"`
	Problem  *Problem        `json:"problem"`
	Stdin    string          `json:"stdin"`
}

func main() {
	cli, err := client.NewEnvClient()
	if err != nil {
		fmt.Printf("Error: %v", err)
		return
	}
	raw := `{"problem":{"id":""},"language":"cpp","stdin":"","files":[{"name":"main.cpp","content":"# include <iostream>\n\nint main() {\n    std::cout << \"hello\\n\";\n    std::cout << \"hello\";\n}"}]}`
	p := &Payload{}
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(p); err != nil {
		fmt.Printf("Error: %v", err)
		return
	}
	dockerEval(context.Background(), cli, p)
}

func writeConn(conn io.Writer, data []byte) error {
	log.Printf("Want to write %d bytes", len(data))
	var start, c int
	var err error
	for {
		if c, err = conn.Write(data[start:]); err != nil {
			return err
		}
		start += c
		log.Printf("Wrote %d of %d bytes", start, len(data))
		if c == 0 || start == len(data) {
			break
		}
	}
	return nil
}

func writeLine(w io.Writer, text string) error {
	n, err := w.Write([]byte(text + "\n"))
	if n != len(text)+1 || (err != nil && err != io.EOF) {
		errorMsg := fmt.Sprintf("Error while writing %d bytes, wrote only %d bytes. Err: %v", len(text)+1, n, err)
		return errors.New(errorMsg)
	}
	return nil
}

type DockerEvalResult struct {
	containerId string
	Done        chan struct{}
	Stdout      io.ReadCloser
	Stderr      io.ReadCloser
	Cancel      context.CancelFunc
	Cleanup     func() error
}

func dockerEval(ctx context.Context, cli *client.Client, payload *Payload) (*DockerEvalResult, error) {
	cfg := configMap[payload.Language]
	config := &container.Config{
		Image:       cfg.Image,
		Cmd:         cfg.Cmd,
		AttachStdin: true,
		OpenStdin:   true,
		StdinOnce:   false,
	}

	resp, err := cli.ContainerCreate(ctx, config, &container.HostConfig{}, &network.NetworkingConfig{}, "")
	if err != nil {
		return nil, err
	}
	containerId := resp.ID

	defer func() {
		log.Infof("Cleaning up docker container %s", containerId)
		//cli.ContainerRemove(context.Background(), containerId, types.ContainerRemoveOptions{Force: true})
	}()

	err = cli.ContainerStart(ctx, containerId, types.ContainerStartOptions{})
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	hijackedResp, err := cli.ContainerAttach(ctx, containerId, types.ContainerAttachOptions{
		Stdin:  true,
		Stream: true,
	})
	if err != nil {
		return nil, err
	}
	go func(data []byte, conn net.Conn) {
		defer func() { log.Printf("done writing to attached stdin") }()
		defer conn.Close()
		err := writeConn(conn, data)
		if err != nil {
			log.Printf("Error while writing to connection: %v", err)
		}
	}(data, hijackedResp.Conn)

	cli.ContainerWait(ctx, containerId)
	stdout, err := cli.ContainerLogs(ctx, containerId, types.ContainerLogsOptions{
		ShowStdout: true,
		Follow:     true,
	})
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		log.Infof("Text: %q", scanner.Text())
		if err := scanner.Err(); err != nil {
			log.Fatalf("scanner err: %v", err)
			return nil, err
		}
	}
	return nil, nil
}
