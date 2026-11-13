//go:build linux && cgo

package emquant

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
typedef struct { char *p; unsigned n; } eqchar;
typedef struct { eqchar *p; unsigned n; } eqchars;
typedef union { char c; _Bool b; short s; unsigned short us; int i; unsigned ui; long long ll; unsigned long long ull; float f; double d; } eqvalue;
typedef struct { int type; eqvalue value; eqchar text; } eqvariant;
typedef struct { eqvariant *p; unsigned n; } eqvariants;
typedef struct { eqchars codes, indicators, dates; eqvariants values; } eqdata;
typedef struct { char user[255], password[255]; } eqlogin;
typedef struct { int version, type, err, request, serial; eqdata *data; } eqmsg;
typedef int (*eq_log_callback)(const char *);
typedef int (*eq_data_callback)(const eqmsg *, void *);
typedef struct {
 void *handle;
 void (*setdir)(const char *);
 int (*setcallback)(eq_data_callback);
 int (*start)(eqlogin *, const char *, eq_log_callback);
 int (*stop)(void);
 int (*csd)(const char *, const char *, const char *, const char *, const char *, eqdata **);
 int (*css)(const char *, const char *, const char *, eqdata **);
 int (*csqsnapshot)(const char *, const char *, const char *, eqdata **);
 int (*cfn)(const char *, const char *, int, const char *, eqdata **);
 int (*tradedates)(const char *, const char *, const char *, eqdata **);
 int (*release)(void *);
} eqapi;
static int log_cb(const char *message) { (void)message; return 1; }
static _Atomic int callback_error;
static int event_cb(const eqmsg *message, void *user) {
 (void)user;
 if (message && (message->type == 0 || message->type == 3) && message->err != 0) atomic_store(&callback_error, message->err);
 return 1;
}
static int eq_callback_error(void) { return atomic_load(&callback_error); }
static int eq_open(eqapi *a, const char *path) {
 memset(a, 0, sizeof(*a));
 a->handle = dlopen(path, RTLD_NOW | RTLD_LOCAL);
 if (!a->handle) return -1;
 a->setdir = dlsym(a->handle, "setserverlistdir");
 a->setcallback = dlsym(a->handle, "setcallback");
 a->start = dlsym(a->handle, "start"); a->stop = dlsym(a->handle, "stop");
 a->csd = dlsym(a->handle, "csd"); a->css = dlsym(a->handle, "css"); a->csqsnapshot = dlsym(a->handle, "csqsnapshot"); a->tradedates = dlsym(a->handle, "tradedates"); a->release = dlsym(a->handle, "releasedata");
 a->cfn = dlsym(a->handle, "cfn");
 if (!a->setdir || !a->setcallback || !a->start || !a->stop || !a->csd || !a->css || !a->csqsnapshot || !a->tradedates || !a->release) {
  dlclose(a->handle); memset(a, 0, sizeof(*a)); return -2;
 }
 return 0;
}
static int eq_start(eqapi *a, const char *dir, const char *user, const char *password) {
 eqlogin login = {0};
 if (strlen(user) >= sizeof(login.user) || strlen(password) >= sizeof(login.password)) return -3;
 memcpy(login.user, user, strlen(user)); memcpy(login.password, password, strlen(password));
 atomic_store(&callback_error, 0);
 a->setdir(dir);
 int rc = a->setcallback(event_cb);
 if (rc == 0) rc = a->start(&login, "LANGUAGEVERSION=503,ForceLogin=0,RecordLoginInfo=0", log_cb);
 volatile char *p = (volatile char*)&login;
 for (size_t i = 0; i < sizeof(login); i++) p[i] = 0;
 return rc;
}
static int eq_query(eqapi *a, const char *codes, const char *indicators, const char *from, const char *to, const char *options, eqdata **out) {
 return a->csd(codes, indicators, from, to, options, out);
}
static int eq_snapshot(eqapi *a, const char *codes, const char *indicators, const char *options, eqdata **out) {
 return a->css(codes, indicators, options, out);
}
static int eq_quote(eqapi *a, const char *codes, const char *indicators, const char *options, eqdata **out) {
 return a->csqsnapshot(codes, indicators, options, out);
}
static int eq_calendar(eqapi *a, const char *from, const char *to, const char *options, eqdata **out) {
 return a->tradedates(from, to, options, out);
}
static int eq_news(eqapi *a, const char *codes, const char *content, int mode, const char *options, eqdata **out) {
 if (!a->cfn) return -4;
 return a->cfn(codes, content, mode, options, out);
}
static int eq_release(eqapi *a, eqdata *d) { return a->release(d); }
static int eq_stop(eqapi *a) { return a->stop(); }
static void eq_unload(eqapi *a) { if (a->handle) dlclose(a->handle); memset(a, 0, sizeof(*a)); }
static long long eq_signed(eqvariant v) { switch(v.type) { case 1: return v.value.c; case 3: return v.value.b; case 4: return v.value.s; case 6: return v.value.i; case 8: return v.value.ll; default: return 0; } }
static unsigned long long eq_unsigned(eqvariant v) { switch(v.type) { case 2: return (unsigned char)v.value.c; case 5: return v.value.us; case 7: return v.value.ui; case 9: return v.value.ull; default: return 0; } }
static double eq_float(eqvariant v) { return v.type == 10 ? v.value.f : v.value.d; }
static int eq_terminated(eqchar s) { return s.p && s.n && s.p[s.n-1] == '\0' && memchr(s.p, '\0', s.n-1) == NULL; }
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"
)

// SDK state is process-global in the vendor wrapper. Limit this package to one
// active session and serialize synchronous calls until concurrency is proven.
var nativeMu sync.Mutex
var nativeCurrent *Client

type Client struct {
	api     C.eqapi
	closed  bool
	started bool
}

// Open loads the external SDK from its runtime directory. Credentials must be
// supplied by the caller's secret mechanism, never as command-line arguments.
func Open(library, user, password string) (*Client, error) {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	if nativeCurrent != nil {
		return nil, errors.New("emquant: session already active")
	}
	if !filepath.IsAbs(library) || user == "" || password == "" || strings.ContainsAny(library+user+password, "\x00") {
		return nil, errors.New("emquant: invalid library path or credentials")
	}
	c := &Client{}
	path := C.CString(library)
	defer C.free(unsafe.Pointer(path))
	if rc := C.eq_open(&c.api, path); rc != 0 {
		return nil, fmt.Errorf("emquant: library load failed (%d)", int(rc))
	}
	dir := C.CString(filepath.Dir(library))
	defer C.free(unsafe.Pointer(dir))
	u := C.CString(user)
	defer C.free(unsafe.Pointer(u))
	p := C.CString(password)
	defer func() { C.memset(unsafe.Pointer(p), 0, C.size_t(len(password))); C.free(unsafe.Pointer(p)) }()
	if rc := C.eq_start(&c.api, dir, u, p); rc != 0 {
		C.eq_unload(&c.api)
		return nil, &SDKError{Operation: "login", Code: int(rc)}
	}
	if rc := C.eq_callback_error(); rc != 0 {
		if stopRC := C.eq_stop(&c.api); stopRC != 0 {
			// The SDK might still be running; keep the library resident and
			// block new sessions until the process is restarted.
			c.started = true
			nativeCurrent = c
			return nil, errors.Join(&SDKError{Operation: "connection callback", Code: int(rc)}, &SDKError{Operation: "stop", Code: int(stopRC)})
		}
		C.eq_unload(&c.api)
		return nil, &SDKError{Operation: "connection callback", Code: int(rc)}
	}
	c.started = true
	nativeCurrent = c
	return c, nil
}

// CSD is synchronous. SDK calls cannot be cancelled until the vendor exposes
// a verified cancellation mechanism; callers must isolate long-running calls.
func (c *Client) CSD(codes, indicators, from, to, options string) (cube Cube, err error) {
	return c.query(0, codes, indicators, from, to, options)
}

// CSS obtains a cross-sectional response. Its dates and dimensions are
// retained as returned; the caller must validate the requested as-of semantics.
func (c *Client) CSS(codes, indicators, options string) (Cube, error) {
	return c.query(1, codes, indicators, "", "", options)
}

// QuoteSnapshot requests a synchronous market snapshot. ObservedAt is the
// local completion time, not the exchange's event time; callers must request
// and validate the vendor's date/time fields and freshness independently.
func (c *Client) QuoteSnapshot(codes, indicators, options string) (Quote, error) {
	cube, err := c.query(2, codes, indicators, "", "", options)
	if err != nil {
		return Quote{}, err
	}
	return Quote{Data: cube, ObservedAt: time.Now().UTC()}, nil
}

// TradeDates returns the raw calendar response. Callers must validate market,
// requested interval and coverage before treating absence as a closed day.
func (c *Client) TradeDates(from, to, options string) (Cube, error) {
	return c.query(3, "", "", from, to, options)
}

// CFN fetches untrusted news or announcement metadata. The result must be
// archived and its source times and entitlements validated before use.
func (c *Client) CFN(codes, content string, mode int, options string) (Cube, error) {
	if mode != 1 && mode != 2 {
		return Cube{}, errors.New("emquant: invalid news mode")
	}
	return c.query(4, codes, content, string(rune('0'+mode)), "", options)
}

func (c *Client) query(mode int, codes, indicators, from, to, options string) (cube Cube, err error) {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	if c == nil || c.closed || !c.started || nativeCurrent != c {
		return Cube{}, errors.New("emquant: session closed")
	}
	if rc := C.eq_callback_error(); rc != 0 {
		return Cube{}, &SDKError{Operation: "connection callback", Code: int(rc)}
	}
	for _, s := range []string{codes, indicators, from, to, options} {
		if strings.ContainsRune(s, 0) {
			return Cube{}, errors.New("emquant: NUL in query")
		}
	}
	if (mode != 3 && (codes == "" || indicators == "")) || ((mode == 0 || mode == 3) && (from == "" || to == "")) {
		return Cube{}, errors.New("emquant: incomplete query")
	}
	if mode != 3 {
		if err := validateIndicators(indicators); err != nil {
			return Cube{}, err
		}
	}
	if err := validateOptions(options); err != nil {
		return Cube{}, err
	}
	a := C.CString(codes)
	defer C.free(unsafe.Pointer(a))
	b := C.CString(indicators)
	defer C.free(unsafe.Pointer(b))
	d := C.CString(from)
	defer C.free(unsafe.Pointer(d))
	e := C.CString(to)
	defer C.free(unsafe.Pointer(e))
	f := C.CString(options)
	defer C.free(unsafe.Pointer(f))
	var data *C.eqdata
	var rc C.int
	switch mode {
	case 3:
		rc = C.eq_calendar(&c.api, d, e, f, &data)
	case 2:
		rc = C.eq_quote(&c.api, a, b, f, &data)
	case 1:
		rc = C.eq_snapshot(&c.api, a, b, f, &data)
	case 4:
		rc = C.eq_news(&c.api, a, b, C.int(from[0]-'0'), f, &data)
	default:
		rc = C.eq_query(&c.api, a, b, d, e, f, &data)
	}
	if rc != 0 {
		return Cube{}, &SDKError{Operation: "query", Code: int(rc)}
	}
	if data == nil {
		return Cube{}, errors.New("emquant: null response")
	}
	defer func() {
		if rc := C.eq_release(&c.api, data); rc != 0 {
			cube = Cube{}
			err = &SDKError{Operation: "release", Code: int(rc)}
		}
	}()
	cube, err = decode(data)
	if rc := C.eq_callback_error(); rc != 0 {
		return Cube{}, &SDKError{Operation: "connection callback", Code: int(rc)}
	}
	return cube, err
}

func decode(data *C.eqdata) (Cube, error) {
	const limit = 1000000
	readString := func(value C.eqchar) (string, error) {
		if value.n == 0 || value.n > limit+1 || C.eq_terminated(value) == 0 {
			return "", fmt.Errorf("%w: invalid SDK string length or terminator", ErrShape)
		}
		s := C.GoStringN(value.p, C.int(value.n-1))
		if !utf8.ValidString(s) {
			return "", fmt.Errorf("%w: invalid UTF-8", ErrShape)
		}
		return s, nil
	}
	if data.codes.n > limit || data.indicators.n > limit || data.dates.n > limit || data.values.n > limit {
		return Cube{}, fmt.Errorf("%w: response too large", ErrShape)
	}
	if (data.codes.n > 0 && data.codes.p == nil) || (data.indicators.n > 0 && data.indicators.p == nil) || (data.dates.n > 0 && data.dates.p == nil) || (data.values.n > 0 && data.values.p == nil) {
		return Cube{}, fmt.Errorf("%w: null array", ErrShape)
	}
	stringsOf := func(a C.eqchars) ([]string, error) {
		out := make([]string, 0, int(a.n))
		for _, x := range unsafe.Slice(a.p, int(a.n)) {
			value, err := readString(x)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	}
	var c Cube
	var err error
	if c.Codes, err = stringsOf(data.codes); err != nil {
		return Cube{}, err
	}
	if c.Indicators, err = stringsOf(data.indicators); err != nil {
		return Cube{}, err
	}
	if c.Dates, err = stringsOf(data.dates); err != nil {
		return Cube{}, err
	}
	for _, v := range unsafe.Slice(data.values.p, int(data.values.n)) {
		cell := Cell{Type: int(v._type)}
		switch cell.Type {
		case 0:
		case 1, 3, 4, 6, 8:
			cell.Value = int64(C.eq_signed(v))
		case 2, 5, 7, 9:
			cell.Value = uint64(C.eq_unsigned(v))
		case 10, 11:
			x := float64(C.eq_float(v))
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return Cube{}, fmt.Errorf("%w: non-finite number", ErrShape)
			}
			cell.Value = x
		case 13, 14:
			value, err := readString(v.text)
			if err != nil {
				return Cube{}, err
			}
			cell.Value = value
		default:
			return Cube{}, fmt.Errorf("%w: unsupported variant type %d", ErrShape, cell.Type)
		}
		c.Values = append(c.Values, cell)
	}
	if len(c.Codes) > 0 && len(c.Indicators) > 0 && len(c.Dates) > 0 {
		if _, err = c.At(0, 0, 0); err != nil {
			return Cube{}, err
		}
	} else if len(c.Values) != 0 {
		return Cube{}, fmt.Errorf("%w: empty axis", ErrShape)
	}
	return c, nil
}

func (c *Client) Close() error {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	if c == nil || c.closed || !c.started {
		return nil
	}
	if nativeCurrent != c {
		return errors.New("emquant: invalid session")
	}
	rc := C.eq_stop(&c.api)
	if rc != 0 {
		return &SDKError{Operation: "stop", Code: int(rc)}
	}
	C.eq_unload(&c.api)
	c.closed = true
	c.started = false
	nativeCurrent = nil
	return nil
}
