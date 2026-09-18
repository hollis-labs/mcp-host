package inprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	sdksub "github.com/hollis-labs/plugin-sdk/subprocess"
)

// ErrGone reports that the plugin subprocess's pipe closed — exited,
// crashed, or killed — while a call was outstanding or before one was
// sent.
var ErrGone = errors.New("inprocess: plugin is gone")

// rpcTransport drives plugin-sdk/subprocess's JSON-RPC dialect as the
// host/client side: newline-delimited JSON-RPC 2.0 requests on the
// subprocess's stdin, responses read from its stdout and matched to
// their waiting caller by request ID. plugin-sdk ships the plugin side
// of this dialect (subprocess.Serve) but not a host-side driver — this
// is mcp-host's own reimplementation of the shape Nanite's
// internal/plugin/subprocess.Transport already proves out (the ADR
// explicitly calls for reimplementing the pattern rather than importing
// Nanite).
type rpcTransport struct {
	w io.Writer
	r *bufio.Reader

	nextID atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan *sdksub.RPCResponse
	closedBy error

	writeMu sync.Mutex
	done    chan struct{}
}

// newRPCTransport wraps r/w (the plugin subprocess's stdout/stdin pipes)
// and starts the background reader loop routing responses to callers.
func newRPCTransport(r io.Reader, w io.Writer) *rpcTransport {
	t := &rpcTransport{
		w:       w,
		r:       bufio.NewReaderSize(r, 64*1024),
		pending: make(map[int64]chan *sdksub.RPCResponse),
		done:    make(chan struct{}),
	}
	go t.readLoop()
	return t
}

// Done returns a channel closed once the transport's read loop ends —
// the plugin's stdout pipe hit EOF or a read error, i.e. the subprocess
// is gone. The supervision loop blocks on this the same way T3's
// process-mode SpawnTransport blocks on an MCP ClientSession's Wait.
func (t *rpcTransport) Done() <-chan struct{} { return t.done }

// closeWriter closes the underlying stdin pipe, if it implements
// io.Closer — the JSON-RPC dialect equivalent of the MCP stdio shutdown
// sequence's "close the input stream to the child process" step. A
// well-behaved plugin's own read loop sees EOF and exits on its own;
// this is what lets Close's reap complete quickly instead of always
// riding out the full SIGTERM/SIGKILL escalation.
func (t *rpcTransport) closeWriter() {
	if closer, ok := t.w.(io.Closer); ok {
		_ = closer.Close()
	}
}

func (t *rpcTransport) call(ctx context.Context, method string, params any) (*sdksub.RPCResponse, error) {
	id := t.nextID.Add(1)
	reply := make(chan *sdksub.RPCResponse, 1)

	t.mu.Lock()
	if t.closedBy != nil {
		err := t.closedBy
		t.mu.Unlock()
		return nil, fmt.Errorf("call %s: %w", method, err)
	}
	t.pending[id] = reply
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	req := sdksub.RPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := t.write(req); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	callCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	select {
	case resp := <-reply:
		if resp == nil {
			return nil, fmt.Errorf("read %s response: %w", method, ErrGone)
		}
		if resp.Error != nil {
			return resp, resp.Error
		}
		return resp, nil
	case <-callCtx.Done():
		return nil, fmt.Errorf("read %s response: %w", method, callCtx.Err())
	case <-t.done:
		t.mu.Lock()
		err := t.closedBy
		t.mu.Unlock()
		if err == nil {
			err = ErrGone
		}
		return nil, fmt.Errorf("read %s response: %w", method, err)
	}
}

func (t *rpcTransport) write(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err = t.w.Write(data)
	return err
}

func (t *rpcTransport) readLoop() {
	var readErr error
	for {
		line, err := t.r.ReadBytes('\n')
		if len(line) > 0 {
			t.deliver(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	t.closeWith(readErr)
}

func (t *rpcTransport) deliver(line []byte) {
	var resp sdksub.RPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return
	}
	t.mu.Lock()
	reply, waiting := t.pending[resp.ID]
	t.mu.Unlock()
	if !waiting {
		return
	}
	select {
	case reply <- &resp:
	default:
	}
}

func (t *rpcTransport) closeWith(cause error) {
	t.mu.Lock()
	if t.closedBy == nil {
		if cause != nil {
			t.closedBy = fmt.Errorf("%w: %w", ErrGone, cause)
		} else {
			t.closedBy = ErrGone
		}
		close(t.done)
	}
	t.pending = make(map[int64]chan *sdksub.RPCResponse)
	t.mu.Unlock()
}

// callResult calls method and unmarshals the response's result into T.
func callResult[T any](ctx context.Context, t *rpcTransport, method string, params any) (*T, error) {
	resp, err := t.call(ctx, method, params)
	if err != nil {
		return nil, err
	}
	var result T
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal %s result: %w", method, err)
	}
	return &result, nil
}
