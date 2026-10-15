package doh

import (
	"encoding/base64"
	"net/netip"
)

type netipAddr = netip.Addr

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func decodeB64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
