package main

import (
    "context"
    "net"
    "sync"
    "io"
    "time"
    "errors"
    "net/http"
    "bufio"
    "crypto/tls"
    "crypto/x509"
    "io/ioutil"
)

const COPY_BUF = 128 * 1024

func proxy(ctx context.Context, left, right net.Conn) {
    wg := sync.WaitGroup{}
    cpy := func (dst, src net.Conn) {
        defer wg.Done()
        io.Copy(dst, src)
        dst.Close()
    }
    wg.Add(2)
    go cpy(left, right)
    go cpy(right, left)
    groupdone := make(chan struct{}, 1)
    go func() {
        wg.Wait()
        groupdone <-struct{}{}
    }()
    select {
    case <-ctx.Done():
        left.Close()
        right.Close()
    case <-groupdone:
        return
    }
    <-groupdone
    return
}

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Connection",
    "Proxy-Authorization",
	"Te", // canonicalized version of "TE"
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func delHopHeaders(header http.Header) {
	for _, h := range hopHeaders {
		header.Del(h)
	}
}

func hijack(hijackable interface{}) (net.Conn, *bufio.ReadWriter, error) {
    hj, ok := hijackable.(http.Hijacker)
    if !ok {
        return nil, nil, errors.New("Connection doesn't support hijacking")
    }
    conn, rw, err := hj.Hijack()
    if err != nil {
        return nil, nil, err
    }
    var emptytime time.Time
    err = conn.SetDeadline(emptytime)
    if err != nil {
        conn.Close()
        return nil, nil, err
    }
    return conn, rw, nil
}

func flush(flusher interface{}) bool {
    f, ok := flusher.(http.Flusher)
    if !ok {
        return false
    }
    f.Flush()
    return true
}

func copyBody(wr io.Writer, body io.Reader) {
    for {
        buf := make([]byte, COPY_BUF)
        bread, read_err := body.Read(buf)
        var write_err error
        if bread > 0 {
            _, write_err = wr.Write(buf[:bread])
            flush(wr)
        }
        if read_err != nil || write_err != nil {
            break
        }
    }
}

func makeServerTLSConfig(certfile, keyfile, cafile string) (*tls.Config, error) {
    var cfg tls.Config
    cert, err := tls.LoadX509KeyPair(certfile, keyfile)
    if err != nil {
        return nil, err
    }
    cfg.Certificates = []tls.Certificate{cert}
    if cafile != "" {
        roots := x509.NewCertPool()
        certs, err := ioutil.ReadFile(cafile)
        if err != nil {
            return nil, err
        }
        if ok := roots.AppendCertsFromPEM(certs); !ok {
            return nil, errors.New("Failed to load CA certificates")
        }
        cfg.ClientCAs = roots
        cfg.ClientAuth = tls.VerifyClientCertIfGiven
    }
    return &cfg, nil
}
