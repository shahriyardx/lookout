package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// Terminal is an OPT-IN full interactive shell exposed over a WebSocket and
// backed by a real PTY. It is a remote shell — only enabled when the operator
// explicitly passes -terminal, and it inherits whatever auth sits in front of
// the server (e.g. the reverse proxy's basic-auth).
//
// cmd is the argv of the shell to spawn. In the containerized deployment this
// is an `nsenter … bash` into the host so the shell is the *host*, not the
// container.

func (s *Server) terminalEnabled() bool { return s.termEnabled && len(s.termCmd) > 0 }

// resizeMsg is the only text (JSON) message the client sends; everything else
// is binary stdin.
type resizeMsg struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func (s *Server) handleTermWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.Command(s.termCmd[0], s.termCmd[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		c.Close(websocket.StatusInternalError, "pty: "+err.Error())
		return
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	}()

	// PTY output -> client (binary frames)
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// client -> PTY: binary = stdin, text = JSON resize
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var m resizeMsg
			if json.Unmarshal(data, &m) == nil && m.Cols > 0 && m.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: m.Rows, Cols: m.Cols})
			}
			continue
		}
		if _, err := ptmx.Write(data); err != nil {
			return
		}
	}
}
