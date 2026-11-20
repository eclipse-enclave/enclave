// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// qmpClient is a QEMU Machine Protocol connection. QEMU closes it when it
// exits, which is how the backend learns that a VM is gone.
type qmpClient struct {
	conn   net.Conn
	closed chan struct{}
	// replies carries command results; events are dropped by the reader.
	replies chan qmpMessage
}

type qmpMessage struct {
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error"`
	Event string `json:"event"`
}

// qmpIOTimeout bounds each exchange with the monitor: the connection, the
// greeting, and every command's write and reply. A QEMU that accepts and then
// says nothing must not hold up a session start, or a stop, beyond this.
var qmpIOTimeout = 10 * time.Second

func dialQMP(ctx context.Context, socket string) (*qmpClient, error) {
	dialCtx, cancel := context.WithTimeout(ctx, qmpIOTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "unix", socket)
	if err != nil {
		return nil, err
	}
	client := &qmpClient{conn: conn, closed: make(chan struct{}), replies: make(chan qmpMessage, 1)}
	reader := bufio.NewReader(conn)
	// The greeting precedes any command.
	_ = conn.SetReadDeadline(time.Now().Add(qmpIOTimeout))
	if _, err := reader.ReadBytes('\n'); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read QMP greeting: %w", err)
	}
	// From here the reader waits for QEMU's exit, however long the VM runs.
	_ = conn.SetReadDeadline(time.Time{})
	go client.read(reader)
	if _, err := client.execute(dialCtx, "qmp_capabilities"); err != nil {
		client.close()
		return nil, err
	}
	return client, nil
}

func (c *qmpClient) read(reader *bufio.Reader) {
	defer close(c.closed)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var message qmpMessage
		if json.Unmarshal(line, &message) != nil || message.Event != "" {
			continue
		}
		select {
		case c.replies <- message:
		default:
		}
	}
}

func (c *qmpClient) execute(ctx context.Context, command string) (json.RawMessage, error) {
	request, err := json.Marshal(map[string]string{"execute": command})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(qmpIOTimeout))
	if _, err := c.conn.Write(append(request, '\n')); err != nil {
		return nil, fmt.Errorf("QMP %s: %w", command, err)
	}
	// Every command used here is answered at once; a monitor that takes the
	// command and stays silent is stuck, and the caller has a fallback.
	select {
	case reply := <-c.replies:
		if reply.Error != nil {
			return nil, fmt.Errorf("QMP %s: %s", command, reply.Error.Desc)
		}
		return reply.Return, nil
	case <-c.closed:
		return nil, fmt.Errorf("QMP %s: connection closed", command)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(qmpIOTimeout):
		return nil, fmt.Errorf("QMP %s: no reply within %s", command, qmpIOTimeout)
	}
}

// waitClosed reports whether QEMU exited within timeout.
func (c *qmpClient) waitClosed(timeout time.Duration) bool {
	select {
	case <-c.closed:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (c *qmpClient) exited() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *qmpClient) close() { _ = c.conn.Close() }
