package repomofo

import (
	"fmt"
	"io"
	"strings"
)

type HandlerKind int

const (
	HandlerGetInfoRefs HandlerKind = iota
	HandlerRunService
)

type HTTPBackendOptions struct {
	RequestMethod   string
	Handler         HandlerKind
	Suffix          string
	QueryString     string
	ContentType     string
	HasRemoteUser   bool
	ProtocolVersion int
}

type httpRoute struct {
	method  string
	suffix  string
	handler HandlerKind
}

var httpRoutes = []httpRoute{
	{"GET", "/info/refs", HandlerGetInfoRefs},
	{"POST", "/git-upload-pack", HandlerRunService},
	{"POST", "/git-receive-pack", HandlerRunService},
}

type httpConfig struct {
	uploadpack  bool
	receivepack bool
}

// ResolveHTTPBackendDir strips the route suffix from path and returns the repo directory.
func ResolveHTTPBackendDir(path string) (string, error) {
	for _, route := range httpRoutes {
		if strings.HasSuffix(path, route.suffix) {
			return path[:len(path)-len(route.suffix)], nil
		}
	}
	return "", fmt.Errorf("not found")
}

// MatchHTTPRoute finds the handler and suffix for a given path.
func MatchHTTPRoute(path string) (HandlerKind, string, bool) {
	for _, route := range httpRoutes {
		if strings.HasSuffix(path, route.suffix) {
			return route.handler, route.suffix, true
		}
	}
	return 0, "", false
}

func (repo *Repo) HTTPBackend(r io.Reader, w io.Writer, options HTTPBackendOptions) error {
	// method validation
	for _, route := range httpRoutes {
		if route.handler == options.Handler {
			if options.RequestMethod != route.method {
				sendBadRequest(w, route.method, options)
				return nil
			}
			break
		}
	}

	err := runRoute(repo, r, w, options)
	if err != nil {
		switch err {
		case errForbidden, errServiceNotEnabled:
			sendForbidden(w)
			return nil
		case errBadRequest, errUnsupportedMediaType:
			return nil
		}
		return err
	}
	return nil
}

var (
	errForbidden            = fmt.Errorf("forbidden")
	errServiceNotEnabled    = fmt.Errorf("service not enabled")
	errBadRequest           = fmt.Errorf("bad request")
	errUnsupportedMediaType = fmt.Errorf("unsupported media type")
)

func runRoute(repo *Repo, r io.Reader, w io.Writer, options HTTPBackendOptions) error {
	config, err := repo.loadConfig()
	if err != nil {
		return err
	}

	httpCfg := httpConfig{uploadpack: true}
	if vars := config.GetSection("http"); vars != nil {
		if v, ok := vars["uploadpack"]; ok {
			httpCfg.uploadpack = parseBoolConfig(v)
		}
		if v, ok := vars["receivepack"]; ok {
			httpCfg.receivepack = parseBoolConfig(v)
		}
	}

	if options.HasRemoteUser {
		httpCfg.receivepack = true
	}

	switch options.Handler {
	case HandlerGetInfoRefs:
		return getInfoRefs(repo, r, w, options, &httpCfg)
	case HandlerRunService:
		return runService(repo, r, w, options, &httpCfg)
	}
	return errBadRequest
}

func httpStatus(w io.Writer, code int, msg string) {
	fmt.Fprintf(w, "Status: %d %s\r\n", code, msg)
}

func writeHeader(w io.Writer, name, value string) {
	fmt.Fprintf(w, "%s: %s\r\n", name, value)
}

func writeNocacheHeaders(w io.Writer) {
	writeHeader(w, "Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
	writeHeader(w, "Pragma", "no-cache")
	writeHeader(w, "Cache-Control", "no-cache, max-age=0, must-revalidate")
}

func finishHeaders(w io.Writer) {
	fmt.Fprintf(w, "\r\n")
}

func SendNotFound(w io.Writer) {
	httpStatus(w, 404, "Not Found")
	writeNocacheHeaders(w)
	finishHeaders(w)
}

func sendForbidden(w io.Writer) {
	httpStatus(w, 403, "Forbidden")
	writeNocacheHeaders(w)
	finishHeaders(w)
}

func sendBadRequest(w io.Writer, allowedMethod string, options HTTPBackendOptions) {
	if options.RequestMethod != allowedMethod {
		httpStatus(w, 405, "Method Not Allowed")
		writeHeader(w, "Allow", allowedMethod)
	} else {
		httpStatus(w, 400, "Bad Request")
	}
	writeNocacheHeaders(w)
	finishHeaders(w)
}

func getInfoRefs(repo *Repo, r io.Reader, w io.Writer, options HTTPBackendOptions, httpCfg *httpConfig) error {
	if strings.HasPrefix(options.QueryString, "service=git-upload-pack") {
		if !httpCfg.uploadpack {
			return errServiceNotEnabled
		}
		httpStatus(w, 200, "OK")
		writeHeader(w, "Content-Type", "application/x-git-upload-pack-advertisement")
		writeNocacheHeaders(w)
		finishHeaders(w)

		if options.ProtocolVersion != 2 {
			writePktLine(w, []byte("# service=git-upload-pack\n"))
			writePktFlush(w)
		}

		return repo.UploadPack(r, w, UploadPackOptions{
			ProtocolVersion: options.ProtocolVersion,
			AdvertiseRefs:   true,
			IsStateless:     true,
		})
	}

	if strings.HasPrefix(options.QueryString, "service=git-receive-pack") {
		if !httpCfg.receivepack {
			return errServiceNotEnabled
		}
		httpStatus(w, 200, "OK")
		writeHeader(w, "Content-Type", "application/x-git-receive-pack-advertisement")
		writeNocacheHeaders(w)
		finishHeaders(w)

		if options.ProtocolVersion != 2 {
			writePktLine(w, []byte("# service=git-receive-pack\n"))
			writePktFlush(w)
		}

		return repo.ReceivePack(r, w, ReceivePackOptions{
			ProtocolVersion: options.ProtocolVersion,
			AdvertiseRefs:   true,
			IsStateless:     true,
		})
	}

	return errBadRequest
}

func runService(repo *Repo, r io.Reader, w io.Writer, options HTTPBackendOptions, httpCfg *httpConfig) error {
	service := ""
	if strings.HasPrefix(options.Suffix, "/git-") {
		service = options.Suffix[len("/git-"):]
	} else {
		return errBadRequest
	}

	isUpload := service == "upload-pack"
	isReceive := service == "receive-pack"

	if !isUpload && !isReceive {
		return errBadRequest
	}

	if isUpload && !httpCfg.uploadpack {
		return errServiceNotEnabled
	}
	if isReceive && !httpCfg.receivepack {
		return errServiceNotEnabled
	}

	expectedCT := fmt.Sprintf("application/x-git-%s-request", service)
	if options.ContentType != expectedCT {
		httpStatus(w, 415, "Unsupported Media Type")
		writeNocacheHeaders(w)
		finishHeaders(w)
		return errUnsupportedMediaType
	}

	resultCT := fmt.Sprintf("application/x-git-%s-result", service)
	httpStatus(w, 200, "OK")
	writeHeader(w, "Content-Type", resultCT)
	writeNocacheHeaders(w)
	finishHeaders(w)

	if isUpload {
		return repo.UploadPack(r, w, UploadPackOptions{
			ProtocolVersion: options.ProtocolVersion,
			IsStateless:     true,
		})
	}

	return repo.ReceivePack(r, w, ReceivePackOptions{
		ProtocolVersion: options.ProtocolVersion,
		IsStateless:     true,
	})
}
