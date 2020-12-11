package bot

import (
	"context"
	"errors"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	hps "github.com/michurin/cnbot/pkg/helpers"
)

const gracefulShutdownInterval = time.Second

type Handler struct {
	BotName   string
	Token     string
	AccessCtl hps.AccessCtl
}

func pathDecode(path string) (int64, error) {
	urlParts := strings.Split(strings.Trim(path, "/"), "/")
	if len(urlParts) != 1 {
		return 0, errors.New("invalid path: " + path)
	}
	user := urlParts[len(urlParts)-1]
	destUser, err := hps.Atoi(user)
	if err != nil {
		return 0, err
	}
	return destUser, nil
}

func readPart(r *multipart.Reader) (string, []byte, bool, error) {
	part, err := r.NextPart()
	if part != nil {
		defer part.Close()
	}
	if err == io.EOF {
		return "", nil, true, nil
	}
	if err != nil {
		return "", nil, true, err
	}
	data, err := ioutil.ReadAll(part)
	if err != nil {
		return "", nil, true, err
	}
	return part.FormName(), data, false, nil
}

func parseMultipart(r *multipart.Reader) (int64, []byte, error) {
	var destUser int64
	var body []byte
	for {
		name, data, done, err := readPart(r)
		if err != nil {
			return 0, nil, err
		}
		if done {
			break
		}
		switch name {
		case "":
			return 0, nil, errors.New("empty param name")
		case "to":
			destUser, err = hps.Atoi(string(data))
			if err != nil {
				return 0, nil, err
			}
		case "msg":
			body = data
		default:
			return 0, nil, errors.New("unknown param name: " + name)
		}
	}
	if body == nil {
		return 0, nil, errors.New("no msg param")
	}
	return destUser, body, nil
}

func multipartBoundary(m string) string {
	// oversimplified:
	// treats errors as non-multipart body
	// treats multipart without boundary as non-multipart body
	contentType, params, err := mime.ParseMediaType(m)
	if err == nil && strings.HasPrefix(contentType, "multipart/") {
		return params["boundary"]
	}
	return ""
}

func decodeRequest(r *http.Request) (int64, []byte, error) {
	if r.Method != http.MethodPost {
		return 0, nil, errors.New("method not allowed")
	}
	var destUser int64
	var body []byte
	var err error
	boundary := multipartBoundary(r.Header.Get("Content-Type"))
	if boundary == "" {
		body, err = ioutil.ReadAll(r.Body)
		if err != nil {
			return 0, nil, err
		}
	} else {
		destUser, body, err = parseMultipart(multipart.NewReader(r.Body, boundary))
		if err != nil {
			return 0, nil, err
		}
	}
	if destUser == 0 {
		destUser, err = pathDecode(r.URL.Path)
		if err != nil {
			return 0, nil, err
		}
	}
	return destUser, body, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := hps.Label(r.Context(), hps.RandLabel(), h.BotName)
	hps.Log(ctx, r.Method, r.URL.String())
	destUser, body, err := decodeRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		hps.Log(ctx, r.URL.String(), err)
		return
	}
	if !h.AccessCtl.IsAllowed(destUser) {
		w.WriteHeader(http.StatusForbidden)
		hps.Log(ctx, destUser, errors.New("user is not allowed"))
		return
	}
	ctx = hps.Label(ctx, destUser)
	err = SmartSend(ctx, h.Token, destUser, body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		hps.Log(ctx, body, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	hps.Log(ctx, http.StatusOK)
}

func RunHTTPServer(ctx context.Context, addr string, writeTimeout time.Duration, readTimeout time.Duration, handler http.Handler) {
	ctx = hps.Label(ctx, "["+addr+"]")
	s := http.Server{
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		ErrorLog:     log.New(os.Stdout, "http", log.LstdFlags|log.Llongfile|log.Lmsgprefix), // TODO establish wrapper for helpers/log.go
		Addr:         addr,
		Handler:      handler,
	}
	go func() { // what if we shutdown before listen?
		<-ctx.Done()
		hps.Log(ctx, "Server is going to shutdown")
		dCtx, cancel := context.WithTimeout(context.Background(), gracefulShutdownInterval)
		defer cancel()
		err := s.Shutdown(dCtx)
		if err != nil {
			hps.Log(ctx, err)
		}
	}()
	hps.Log(ctx, "Server is starting on", s.Addr, "with timeouts", s.ReadTimeout, s.WriteTimeout)
	hps.Log(ctx, s.ListenAndServe())
	hps.Log(ctx, "Server finished")
}
