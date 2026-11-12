//go:build !linux || !cgo

package emquant

import "errors"

type Client struct{}

func Open(_, _, _ string) (*Client, error) {
	return nil, errors.New("emquant: native SDK requires Linux and cgo")
}

func (*Client) CSD(_, _, _, _, _ string) (Cube, error) {
	return Cube{}, errors.New("emquant: native SDK requires Linux and cgo")
}

func (*Client) CSS(_, _, _ string) (Cube, error) {
	return Cube{}, errors.New("emquant: native SDK requires Linux and cgo")
}

func (*Client) Close() error { return nil }
