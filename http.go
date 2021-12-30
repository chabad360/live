package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/sessions"
	"golang.org/x/net/html"
	"nhooyr.io/websocket"
)

var _ Handler = &HTTPHandler{}
var _ Socket = &HTTPSocket{}
var _ HTTPSessionStore = &CookieStore{}

// sessionCookie the name of the session cookie.
const sessionCookie string = "_ls"

// HTTPSessionStore handles storing and retrieving sessions.
type HTTPSessionStore interface {
	Get(*http.Request) (Session, error)
	Save(http.ResponseWriter, *http.Request, Session) error
	Clear(http.ResponseWriter, *http.Request) error
}

// HTTPHandler serves live for net/http.
type HTTPHandler struct {
	sessionStore HTTPSessionStore
	*BaseHandler
}

// NewHandler returns the net/http handler for live.
func NewHandler(store HTTPSessionStore, configs ...HandlerConfig) (*HTTPHandler, error) {
	d, err := NewBaseHandler(configs...)
	if err != nil {
		return nil, fmt.Errorf("could not init base handler: %w", err)
	}
	h := &HTTPHandler{
		sessionStore: store,
		BaseHandler:  d,
	}
	for _, conf := range configs {
		if err := conf(h); err != nil {
			return nil, fmt.Errorf("could not apply config: %w", err)
		}
	}
	return h, nil
}

// ServeHTTP serves this handler.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/favicon.ico" {
		if h.ignoreFaviconRequest {
			w.WriteHeader(404)
			return
		}
	}

	// Check if we are going to upgrade to a webscoket.
	upgrade := false
	for _, header := range r.Header["Upgrade"] {
		if header == "websocket" {
			upgrade = true
			break
		}
	}

	ctx := httpContext(w, r)

	if !upgrade {
		// Serve the http version of the handler.
		h.serveHTTP(ctx, w, r)
		return
	}

	// Upgrade to the webscoket version.
	h.serveWS(ctx, w, r)
}

// serveHTTP serve an http request to the handler.
func (h *HTTPHandler) serveHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request) {

	// Get session.
	session, err := h.sessionStore.Get(r)
	if err != nil {
		if r.URL.Query().Get("live-repair") != "" {
			h.Error()(ctx, fmt.Errorf("session corrupted: %w", err))
			return
		} else {
			log.Println(fmt.Errorf("session corrupted trying to repair: %w", err))
			h.sessionStore.Clear(w, r)
			q := r.URL.Query()
			q.Set("live-repair", "1")
			r.URL.RawQuery = q.Encode()
			http.Redirect(w, r, r.URL.String(), http.StatusTemporaryRedirect)
		}
		return
	}

	// Get socket.
	sock := NewHTTPSocket(session, h, false)

	// Run mount, this generates the state for the page we are on.
	data, err := h.Mount()(ctx, sock)
	if err != nil {
		h.Error()(ctx, err)
		return
	}
	sock.Assign(data)

	// Handle any query parameters that are on the page.
	for _, ph := range h.Params() {
		data, err := ph(ctx, sock, NewParamsFromRequest(r))
		if err != nil {
			h.Error()(ctx, err)
			return
		}
		sock.Assign(data)
	}

	// Render the HTML to display the page.
	render, err := RenderSocket(ctx, h, sock)
	if err != nil {
		h.Error()(ctx, err)
		return
	}
	sock.UpdateRender(render)

	var rendered bytes.Buffer
	html.Render(&rendered, render)

	if err := h.sessionStore.Save(w, r, session); err != nil {
		h.Error()(ctx, err)
		return
	}

	w.WriteHeader(200)
	io.Copy(w, &rendered)
}

// serveWS serve a websocket request to the handler.
func (h *HTTPHandler) serveWS(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// Get the session from the http request.
	session, err := h.sessionStore.Get(r)
	if err != nil {
		h.Error()(ctx, err)
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.Error()(ctx, err)
		return
	}
	defer c.Close(websocket.StatusInternalError, "")
	writeTimeout(ctx, time.Second*5, c, Event{T: EventConnect})
	{
		err := h._serveWS(ctx, r, session, c)
		if errors.Is(err, context.Canceled) {
			return
		}
		switch websocket.CloseStatus(err) {
		case websocket.StatusNormalClosure:
			return
		case websocket.StatusGoingAway:
			return
		default:
			log.Println(fmt.Errorf("ws closed with status (%d): %w", websocket.CloseStatus(err), err))
			return
		}
	}
}

// _serveWS implement the logic for a web socket connection.
func (h *HTTPHandler) _serveWS(ctx context.Context, r *http.Request, session Session, c *websocket.Conn) error {
	// Get the sessions socket and register it with the server.
	sock := NewHTTPSocket(session, h, true)
	sock.assignWS(c)
	h.AddSocket(sock)
	defer h.DeleteSocket(sock)

	// Internal errors.
	internalErrors := make(chan error)

	// Event errors.
	eventErrors := make(chan ErrorEvent)

	// Handle events coming from the websocket connection.
	go func() {
		for {
			t, d, err := c.Read(ctx)
			if err != nil {
				internalErrors <- err
				break
			}
			switch t {
			case websocket.MessageText:
				var m Event
				if err := json.Unmarshal(d, &m); err != nil {
					internalErrors <- err
					break
				}
				switch m.T {
				case EventParams:
					if err := h.CallParams(ctx, sock, m); err != nil {
						switch {
						case errors.Is(err, ErrNoEventHandler):
							log.Println("event error", m, err)
						default:
							eventErrors <- ErrorEvent{Source: m, Err: err.Error()}
						}
					}
				default:
					if err := h.CallEvent(ctx, m.T, sock, m); err != nil {
						switch {
						case errors.Is(err, ErrNoEventHandler):
							log.Println("event error", m, err)
						default:
							eventErrors <- ErrorEvent{Source: m, Err: err.Error()}
						}
					}
				}
				render, err := RenderSocket(ctx, h, sock)
				if err != nil {
					internalErrors <- fmt.Errorf("socket handle error: %w", err)
				} else {
					sock.UpdateRender(render)
				}
				if err := sock.Send(EventAck, nil, WithID(m.ID)); err != nil {
					internalErrors <- fmt.Errorf("socket send error: %w", err)
				}
			case websocket.MessageBinary:
				log.Println("binary messages unhandled")
			}
		}
		close(internalErrors)
		close(eventErrors)
	}()

	// Run mount again now that eh socket is connected, passing true indicating
	// a connection has been made.
	data, err := h.Mount()(ctx, sock)
	if err != nil {
		return fmt.Errorf("socket mount error: %w", err)
	}
	sock.Assign(data)

	// Run params again now that the socket is connected.
	for _, ph := range h.Params() {
		data, err := ph(ctx, sock, NewParamsFromRequest(r))
		if err != nil {
			return fmt.Errorf("socket params error: %w", err)
		}
		sock.Assign(data)
	}

	// Run render now that we are connected for the first time and we have just
	// mounted again. This will generate and send any patches if there have
	// been changes.
	render, err := RenderSocket(ctx, h, sock)
	if err != nil {
		return fmt.Errorf("socket render error: %w", err)
	}
	sock.UpdateRender(render)

	// Send events to the websocket connection.
	for {
		select {
		case msg := <-sock.msgs:
			if err := writeTimeout(ctx, time.Second*5, c, msg); err != nil {
				return fmt.Errorf("writing to socket error: %w", err)
			}
		case ee := <-eventErrors:
			d, err := json.Marshal(ee)
			if err != nil {
				return fmt.Errorf("writing to socket error: %w", err)
			}
			if err := writeTimeout(ctx, time.Second*5, c, Event{T: EventError, Data: d}); err != nil {
				return fmt.Errorf("writing to socket error: %w", err)
			}
		case err := <-internalErrors:
			if err != nil {
				d, err := json.Marshal(err.Error())
				if err != nil {
					return fmt.Errorf("writing to socket error: %w", err)
				}
				if err := writeTimeout(ctx, time.Second*5, c, Event{T: EventError, Data: d}); err != nil {
					return fmt.Errorf("writing to socket error: %w", err)
				}
				// Something catastrophic has happened.
				return fmt.Errorf("internal error: %w", err)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

type HTTPSocket struct {
	*BaseSocket
}

// NewHTTPSocket creates a new http socket.
func NewHTTPSocket(s Session, h Handler, connected bool) *HTTPSocket {
	return &HTTPSocket{
		BaseSocket: NewBaseSocket(s, h, connected),
	}
}

// assignWS connect a web socket to a socket.
func (s *HTTPSocket) assignWS(ws *websocket.Conn) {
	s.closeSlow = func() {
		ws.Close(websocket.StatusPolicyViolation, "socket too slow to keep up with messages")
	}
}

func httpContext(w http.ResponseWriter, r *http.Request) context.Context {
	ctx := r.Context()
	ctx = contextWithRequest(ctx, r)
	ctx = contextWithWriter(ctx, w)
	return ctx
}

func writeTimeout(ctx context.Context, timeout time.Duration, c *websocket.Conn, msg Event) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	data, err := json.Marshal(&msg)
	if err != nil {
		return fmt.Errorf("failed writeTimeout: %w", err)
	}

	return c.Write(ctx, websocket.MessageText, data)
}

// CookieStore a `gorilla/sessions` based cookie store.
type CookieStore struct {
	Store       *sessions.CookieStore
	sessionName string // session name.
}

// NewCookieStore create a new `gorilla/sessions` based cookie store.
func NewCookieStore(sessionName string, keyPairs ...[]byte) *CookieStore {
	s := sessions.NewCookieStore(keyPairs...)
	s.Options.HttpOnly = true
	s.Options.Secure = false
	s.Options.SameSite = http.SameSiteStrictMode

	return &CookieStore{
		Store:       s,
		sessionName: sessionName,
	}
}

// Get get a session.
func (c CookieStore) Get(r *http.Request) (Session, error) {
	var sess Session
	session, err := c.Store.Get(r, c.sessionName)
	if err != nil {
		return NewSession(), err
	}
	vals, ok := session.Values[sessionCookie]
	if !ok {
		// Create new connection.
		ns := NewSession()
		sess = ns
	} else {
		sess, ok = vals.(Session)
		if !ok {
			// Create new session and set.
			ns := NewSession()
			sess = ns
		}
	}
	return sess, nil
}

// Save a session.
func (c CookieStore) Save(w http.ResponseWriter, r *http.Request, session Session) error {
	s, err := c.Store.Get(r, c.sessionName)
	if err != nil {
		return err
	}
	s.Values[sessionCookie] = session
	return s.Save(r, w)
}

// Clear a session.
func (c CookieStore) Clear(w http.ResponseWriter, r *http.Request) error {
	http.SetCookie(w, &http.Cookie{
		Name:     c.sessionName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
	})
	return nil
}
