package tinysocks

import (
	"errors"
	"io"
	"net"
	"strconv"
)

// SOCKS request commands as defined in RFC 1928 section 4.
const (
	CmdConnect      = 1
	CmdBind         = 2
	CmdUDPAssociate = 3
)

// SOCKS address types as defined in RFC 1928 section 5.
const (
	AtypIPv4       = 1
	AtypDomainName = 3
	AtypIPv6       = 4
)

// Error represents a SOCKS error
type Error byte

func (err Error) Error() string {
	return "SOCKS error: " + strconv.Itoa(int(err))
}

// SOCKS errors as defined in RFC 1928 section 6.
const (
	ErrGeneralFailure       = Error(1)
	ErrConnectionNotAllowed = Error(2)
	ErrNetworkUnreachable   = Error(3)
	ErrHostUnreachable      = Error(4)
	ErrConnectionRefused    = Error(5)
	ErrTTLExpired           = Error(6)
	ErrCommandNotSupported  = Error(7)
	ErrAddressNotSupported  = Error(8)
	InfoUDPAssociate        = Error(9)
)

// MaxAddrLen is the maximum size of SOCKS address in bytes.
const MaxAddrLen = 1 + 1 + 255 + 2

// Addr represents a SOCKS address as defined in RFC 1928 section 5.
type Addr []byte

// String serializes SOCKS address a to string form.
func (a Addr) String() string {
	var host, port string

	switch a[0] { // address type
	case AtypDomainName:
		host = string(a[2 : 2+int(a[1])])
		port = strconv.Itoa((int(a[2+int(a[1])]) << 8) | int(a[2+int(a[1])+1]))
	case AtypIPv4:
		host = net.IP(a[1 : 1+net.IPv4len]).String()
		port = strconv.Itoa((int(a[1+net.IPv4len]) << 8) | int(a[1+net.IPv4len+1]))
	case AtypIPv6:
		host = net.IP(a[1 : 1+net.IPv6len]).String()
		port = strconv.Itoa((int(a[1+net.IPv6len]) << 8) | int(a[1+net.IPv6len+1]))
	}

	return net.JoinHostPort(host, port)
}

func readAddr(r io.Reader, b []byte) (Addr, error) {
	if len(b) < MaxAddrLen {
		return nil, io.ErrShortBuffer
	}
	_, err := io.ReadFull(r, b[:1]) // read 1st byte for address type
	if err != nil {
		return nil, err
	}

	switch b[0] {
	case AtypDomainName:
		_, err = io.ReadFull(r, b[1:2]) // read 2nd byte for domain length
		if err != nil {
			return nil, err
		}
		_, err = io.ReadFull(r, b[2:2+int(b[1])+2])
		return b[:1+1+int(b[1])+2], err
	case AtypIPv4:
		_, err = io.ReadFull(r, b[1:1+net.IPv4len+2])
		return b[:1+net.IPv4len+2], err
	case AtypIPv6:
		_, err = io.ReadFull(r, b[1:1+net.IPv6len+2])
		return b[:1+net.IPv6len+2], err
	}

	return nil, ErrAddressNotSupported
}

// ReadAddr reads just enough bytes from r to get a valid Addr.
func ReadAddr(r io.Reader) (Addr, error) {
	return readAddr(r, make([]byte, MaxAddrLen))
}

// SplitAddr slices a SOCKS address from beginning of b. Returns nil if failed.
func SplitAddr(b []byte) Addr {
	addrLen := 1
	if len(b) < addrLen {
		return nil
	}

	switch b[0] {
	case AtypDomainName:
		if len(b) < 2 {
			return nil
		}
		addrLen = 1 + 1 + int(b[1]) + 2
	case AtypIPv4:
		addrLen = 1 + net.IPv4len + 2
	case AtypIPv6:
		addrLen = 1 + net.IPv6len + 2
	default:
		return nil

	}

	if len(b) < addrLen {
		return nil
	}

	return b[:addrLen]
}

// ParseAddr parses the address in string s. Returns nil if failed.
func ParseAddr(s string) Addr {
	var addr Addr
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			addr = make([]byte, 1+net.IPv4len+2)
			addr[0] = AtypIPv4
			copy(addr[1:], ip4)
		} else {
			addr = make([]byte, 1+net.IPv6len+2)
			addr[0] = AtypIPv6
			copy(addr[1:], ip)
		}
	} else {
		if len(host) > 255 {
			return nil
		}
		addr = make([]byte, 1+1+len(host)+2)
		addr[0] = AtypDomainName
		addr[1] = byte(len(host))
		copy(addr[2:], host)
	}

	portnum, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil
	}

	addr[len(addr)-2], addr[len(addr)-1] = byte(portnum>>8), byte(portnum)

	return addr
}

// Handshake fast-tracks SOCKS initialization to get target address to connect.
func ReadRequest(rw io.ReadWriter) (byte, Addr, error) {
	// Read RFC 1928 for request and reply structure and sizes.
	buf := make([]byte, MaxAddrLen)
	// read VER, NMETHODS, METHODS
	if _, err := io.ReadFull(rw, buf[:2]); err != nil {
		return 0, nil, err
	}
	nmethods := buf[1]
	if _, err := io.ReadFull(rw, buf[:nmethods]); err != nil {
		return 0, nil, err
	}
	// write VER METHOD
	if _, err := rw.Write([]byte{5, 0}); err != nil {
		return 0, nil, err
	}
	// read VER CMD RSV ATYP DST.ADDR DST.PORT
	if _, err := io.ReadFull(rw, buf[:3]); err != nil {
		return 0, nil, err
	}
	cmd := buf[1]
	addr, err := readAddr(rw, buf)
	if err != nil {
		return 0, nil, err
	}
	return cmd, addr, err // skip VER, RSV fields
}

// Reply to client abount condition of proxy target
func CompleteRequestTCP(errcode byte, conn io.ReadWriteCloser) error {
	_, err := conn.Write([]byte{5, errcode, 0, 1, 0, 0, 0, 0, 0, 0})
	if err != nil {
		return errors.New("Couldn't complete handshake")
	}
	return nil
}

// Client outbounds SOCKS5 requests.
func Client(rw io.ReadWriter, ad Addr, conntype int) (error, Addr) {
	// Read RFC 1928 for request and reply structure and sizes.
	buf := make([]byte, MaxAddrLen)
	// write VER, NMETHODS, METHODS
	if _, err := rw.Write([]byte{5, 1, 0}); err != nil {
		return err, nil
	}
	// read VER METHOD
	if _, err := io.ReadFull(rw, buf[:2]); err != nil {
		return err, nil
	}
	if buf[0] != 5 || buf[1] != 0 {
		return errors.New("SOCKS server mismatch"), nil
	}
	if conntype == CmdConnect {
		// write VER, CMD, RSV, ATYP, DST.ADDR, DST.PORT
		copy(buf[:], []byte{5, 1, 0})
		copy(buf[3:], ad)
		lentosend := 3 + len(ad)
		if _, err := rw.Write(buf[:lentosend]); err != nil {
			return err, nil
		}
		_, err := io.ReadFull(rw, buf[:3])
		if err != nil {
			return errors.New("Couldn't read server response"), nil
		}
		if buf[1] != 0 {
			err = errors.New("Failed to pass to local proxy")
		}
		readAddr(rw, buf)
		return err, nil
	} else if conntype == CmdUDPAssociate {
		// write VER, CMD, RSV, ATYP, DST.ADDR, DST.PORT
		copy(buf[:], []byte{5, 3, 0})
		copy(buf[3:], ad)
		lentosend := 3 + len(ad)
		if _, err := rw.Write(buf[:lentosend]); err != nil {
			return err, nil
		}
		_, err := io.ReadFull(rw, buf[:3])
		if err != nil {
			return errors.New("Couldn't read server response"), nil
		}
		if buf[1] != 0 {
			err = errors.New("Failed to pass to local proxy")
		}
		addr, err := readAddr(rw, buf)
		return err, addr
	} else {
		return errors.New("SOCKS command unsupported"), nil
	}
}
